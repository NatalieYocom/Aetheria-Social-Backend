package moderation

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/page"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db     *sql.DB
	events *realtime.Broker
}

func NewHandler(db *sql.DB, brokers ...*realtime.Broker) *Handler {
	var broker *realtime.Broker
	if len(brokers) > 0 {
		broker = brokers[0]
	}
	return &Handler{db: db, events: broker}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/reports", h.CreateReport)
		r.Get("/api/me/reports", h.ListMyReports)

		r.Get("/api/moderation/reports", h.ListReports)
		r.Patch("/api/moderation/reports/{id}", h.UpdateReport)
		r.Get("/api/moderation/actions", h.ListActions)
		r.Post("/api/moderation/users/{actorId}/actions", h.ApplyUserAction)
		r.Get("/api/moderation/domain-blocks", h.ListDomainBlocks)
		r.Post("/api/moderation/domain-blocks", h.UpsertDomainBlock)
		r.Delete("/api/moderation/domain-blocks/{domain}", h.DeleteDomainBlock)
	})
}

const currentUserRoleSQL = `
SELECT role
FROM users
WHERE id = $1 AND status = 'active'`

type createReportRequest struct {
	TargetActorID   *uuid.UUID `json:"targetActorId"`
	TargetObjectURI string     `json:"targetObjectUri"`
	Reason          string     `json:"reason"`
}

type updateReportRequest struct {
	State string `json:"state"`
}

type userActionRequest struct {
	Action   string     `json:"action"`
	Reason   string     `json:"reason"`
	ReportID *uuid.UUID `json:"reportId"`
}

type ModerationActionResponse struct {
	ID              uuid.UUID  `json:"id"`
	ModeratorUserID uuid.UUID  `json:"moderatorUserId"`
	TargetUserID    uuid.UUID  `json:"targetUserId"`
	TargetActorID   uuid.UUID  `json:"targetActorId"`
	Action          string     `json:"action"`
	Reason          string     `json:"reason"`
	ReportID        *uuid.UUID `json:"reportId,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

const moderationTargetSQL = `
SELECT u.id, a.id, u.role, u.status
FROM users u
JOIN actors a ON a.local_user_id = u.id
WHERE a.id = $1
FOR UPDATE OF u`

type upsertDomainBlockRequest struct {
	Domain   string `json:"domain"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

type ReportResponse struct {
	ID              uuid.UUID  `json:"id"`
	ReporterActorID uuid.UUID  `json:"reporterActorId"`
	TargetActorID   *uuid.UUID `json:"targetActorId,omitempty"`
	TargetObjectURI string     `json:"targetObjectUri,omitempty"`
	Reason          string     `json:"reason"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type DomainBlockResponse struct {
	ID        uuid.UUID `json:"id"`
	Domain    string    `json:"domain"`
	Severity  string    `json:"severity"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *Handler) CreateReport(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createReportRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.TargetObjectURI = strings.TrimSpace(req.TargetObjectURI)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.TargetActorID == nil && req.TargetObjectURI == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "targetActorId or targetObjectUri is required")
		return
	}
	if req.TargetActorID != nil && *req.TargetActorID == principal.ActorID {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "cannot report yourself")
		return
	}
	if req.Reason == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_reason", "reason is required")
		return
	}

	report, err := scanReport(h.db.QueryRowContext(r.Context(), `
INSERT INTO reports (reporter_actor_id, target_actor_id, target_object_uri, reason)
VALUES ($1, $2, $3, $4)
RETURNING id, reporter_actor_id, target_actor_id, target_object_uri, reason, state, created_at`,
		principal.ActorID,
		dbx.NullUUID(req.TargetActorID),
		nullString(req.TargetObjectURI),
		req.Reason,
	))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_report_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, report)
}

func (h *Handler) ListMyReports(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	requestPage, err := page.ParseRequest(r, 50, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	cursorTime, cursorID := timeCursorArgs(requestPage.Cursor)
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, reporter_actor_id, target_actor_id, target_object_uri, reason, state, created_at
FROM reports
WHERE reporter_actor_id = $1
  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
ORDER BY created_at DESC, id DESC
LIMIT $4`, principal.ActorID, cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_reports_failed", err.Error())
		return
	}
	defer rows.Close()
	writeReports(w, rows, requestPage)
}

func (h *Handler) ListReports(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	state := normalizeReportState(r.URL.Query().Get("state"))
	if state == "" {
		state = "open"
	}
	requestPage, err := page.ParseRequest(r, 50, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	cursorTime, cursorID := timeCursorArgs(requestPage.Cursor)
	rows, err := h.db.QueryContext(r.Context(), `
SELECT r.id, r.reporter_actor_id, r.target_actor_id, r.target_object_uri, r.reason, r.state, r.created_at
FROM reports r
WHERE r.state = $1
  AND ($2::timestamptz IS NULL OR (r.created_at, r.id) < ($2, $3))
ORDER BY r.created_at DESC, r.id DESC
LIMIT $4`, state, cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_reports_failed", err.Error())
		return
	}
	defer rows.Close()
	writeReports(w, rows, requestPage)
}

func (h *Handler) UpdateReport(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req updateReportRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	state := normalizeReportState(req.State)
	if state == "" || state == "open" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_state", "state must be reviewed, resolved or rejected")
		return
	}
	report, err := scanReport(h.db.QueryRowContext(r.Context(), `
UPDATE reports
SET state = $2
WHERE id = $1
RETURNING id, reporter_actor_id, target_actor_id, target_object_uri, reason, state, created_at`, id, state))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "report not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "update_report_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, report)
}

func (h *Handler) ApplyUserAction(w http.ResponseWriter, r *http.Request) {
	principal, moderatorRole, ok := h.requireModeratorIdentity(w, r)
	if !ok {
		return
	}
	targetActorID, ok := parseUUIDParam(w, r, "actorId")
	if !ok {
		return
	}
	if targetActorID == principal.ActorID {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "cannot moderate yourself")
		return
	}
	var req userActionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Action != "suspend" && req.Action != "restore" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_action", "action must be suspend or restore")
		return
	}
	if req.Reason == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_reason", "reason is required")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
		return
	}
	defer tx.Rollback()
	var targetUserID, resolvedActorID uuid.UUID
	var targetRole, currentStatus string
	if err := tx.QueryRowContext(r.Context(), moderationTargetSQL, targetActorID).Scan(
		&targetUserID, &resolvedActorID, &targetRole, &currentStatus,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "local user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
		return
	}
	if targetRole == "admin" || (targetRole == "moderator" && moderatorRole != "admin") {
		httpx.WriteError(w, http.StatusForbidden, "protected_account", "insufficient role to moderate this account")
		return
	}
	if req.ReportID != nil {
		var reportMatches bool
		if err := tx.QueryRowContext(r.Context(), `
SELECT EXISTS (
  SELECT 1 FROM reports WHERE id = $1 AND target_actor_id = $2
)`, *req.ReportID, targetActorID).Scan(&reportMatches); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
		if !reportMatches {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_report", "report does not target this actor")
			return
		}
	}
	targetStatus := "suspended"
	if req.Action == "restore" {
		targetStatus = "active"
	}
	if currentStatus == targetStatus {
		httpx.WriteError(w, http.StatusConflict, "status_unchanged", "account already has the requested status")
		return
	}
	statusUpdateSQL := `UPDATE users SET status = $2 WHERE id = $1`
	if req.Action == "suspend" {
		statusUpdateSQL = `UPDATE users SET status = $2, auth_version = auth_version + 1 WHERE id = $1`
	}
	if _, err := tx.ExecContext(r.Context(), statusUpdateSQL, targetUserID, targetStatus); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
		return
	}
	if req.Action == "suspend" {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM presence_sessions WHERE actor_id = $1`, targetActorID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
		rows, err := tx.QueryContext(r.Context(), `
UPDATE instance_members
SET state = 'left', left_at = now(), last_seen_at = now()
WHERE actor_id = $1 AND state = 'joined'
RETURNING instance_id`, targetActorID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
		instanceIDs := []uuid.UUID{}
		for rows.Next() {
			var instanceID uuid.UUID
			if err := rows.Scan(&instanceID); err != nil {
				rows.Close()
				httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
				return
			}
			instanceIDs = append(instanceIDs, instanceID)
		}
		if err := rows.Close(); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
		for _, instanceID := range instanceIDs {
			if _, err := tx.ExecContext(r.Context(), `
UPDATE instances
SET current_users = GREATEST(current_users - 1, 0)
WHERE id = $1`, instanceID); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(), `
UPDATE instance_join_tickets
SET expires_at = now()
WHERE actor_id = $1 AND consumed_at IS NULL AND expires_at > now()`, targetActorID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
	}
	action, err := scanModerationAction(tx.QueryRowContext(r.Context(), `
INSERT INTO moderation_actions (moderator_user_id, target_user_id, target_actor_id, action, reason, report_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, moderator_user_id, target_user_id, target_actor_id, action, reason, report_id, created_at`,
		principal.UserID, targetUserID, targetActorID, req.Action, req.Reason, dbx.NullUUID(req.ReportID)))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
		return
	}
	if req.ReportID != nil {
		if _, err := tx.ExecContext(r.Context(), `UPDATE reports SET state = 'resolved' WHERE id = $1`, *req.ReportID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "moderation_action_failed", err.Error())
		return
	}
	eventType := "user.restored"
	if req.Action == "suspend" {
		eventType = "user.suspended"
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{targetActorID}, eventType, principal.ActorID, map[string]any{
		"actorId": targetActorID, "actionId": action.ID,
	})
	httpx.WriteJSON(w, http.StatusOK, action)
}

func (h *Handler) ListActions(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	requestPage, err := page.ParseRequest(r, 50, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	cursorTime, cursorID := timeCursorArgs(requestPage.Cursor)
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, moderator_user_id, target_user_id, target_actor_id, action, reason, report_id, created_at
FROM moderation_actions
WHERE $1::timestamptz IS NULL OR (created_at, id) < ($1, $2)
ORDER BY created_at DESC, id DESC
LIMIT $3`, cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_moderation_actions_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []ModerationActionResponse{}
	for rows.Next() {
		item, err := scanModerationAction(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_moderation_action_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_moderation_actions_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.CreatedAt, ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[ModerationActionResponse]{
		Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) ListDomainBlocks(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	requestPage, err := page.ParseTextRequest(r, 100, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	var cursorText any
	cursorID := uuid.Nil
	if requestPage.Cursor != nil {
		cursorText = requestPage.Cursor.SortText
		cursorID = requestPage.Cursor.ID
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, domain, severity, reason, created_at
FROM domain_blocks
WHERE $1::text IS NULL OR (lower(domain), id) > ($1, $2)
ORDER BY lower(domain), id
LIMIT $3`, cursorText, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_domain_blocks_failed", err.Error())
		return
	}
	defer rows.Close()

	items := []DomainBlockResponse{}
	for rows.Next() {
		item, err := scanDomainBlock(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_domain_block_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_domain_blocks_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextTextCursor(page.TextCursor{SortText: strings.ToLower(last.Domain), ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[DomainBlockResponse]{
		Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) UpsertDomainBlock(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	var req upsertDomainBlockRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	domain := normalizeDomain(req.Domain)
	if domain == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_domain", "domain is required")
		return
	}
	severity := normalizeDomainBlockSeverity(req.Severity)
	if severity == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_severity", "severity must be silence, suspend, reject_media or reject_all")
		return
	}
	block, err := scanDomainBlock(h.db.QueryRowContext(r.Context(), `
INSERT INTO domain_blocks (domain, severity, reason)
VALUES ($1, $2, $3)
ON CONFLICT (domain)
DO UPDATE SET severity = EXCLUDED.severity, reason = EXCLUDED.reason
RETURNING id, domain, severity, reason, created_at`, domain, severity, strings.TrimSpace(req.Reason)))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "upsert_domain_block_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, block)
}

func (h *Handler) DeleteDomainBlock(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	domain, err := url.PathUnescape(chi.URLParam(r, "domain"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_domain", "domain is invalid")
		return
	}
	domain = normalizeDomain(domain)
	if domain == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_domain", "domain is required")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM domain_blocks WHERE domain = $1`, domain); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "delete_domain_block_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireModerator(w http.ResponseWriter, r *http.Request) bool {
	_, _, ok := h.requireModeratorIdentity(w, r)
	return ok
}

func (h *Handler) requireModeratorIdentity(w http.ResponseWriter, r *http.Request) (auth.Principal, string, bool) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, "", false
	}
	var role string
	if err := h.db.QueryRowContext(r.Context(), currentUserRoleSQL, principal.UserID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "moderator role required")
			return auth.Principal{}, "", false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "role_check_failed", err.Error())
		return auth.Principal{}, "", false
	}
	if role != "moderator" && role != "admin" {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "moderator role required")
		return auth.Principal{}, "", false
	}
	return principal, role, true
}

func writeReports(w http.ResponseWriter, rows *sql.Rows, requestPage page.Request) {
	items := []ReportResponse{}
	for rows.Next() {
		item, err := scanReport(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_report_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_reports_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.CreatedAt, ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[ReportResponse]{
		Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func timeCursorArgs(cursor *page.Cursor) (any, uuid.UUID) {
	if cursor == nil {
		return nil, uuid.Nil
	}
	return cursor.SortTime, cursor.ID
}

type scanner interface {
	Scan(dest ...any) error
}

func scanReport(row scanner) (ReportResponse, error) {
	var report ReportResponse
	var targetActorID uuid.NullUUID
	var targetObjectURI sql.NullString
	if err := row.Scan(
		&report.ID,
		&report.ReporterActorID,
		&targetActorID,
		&targetObjectURI,
		&report.Reason,
		&report.State,
		&report.CreatedAt,
	); err != nil {
		return ReportResponse{}, err
	}
	report.TargetActorID = dbx.UUIDPtr(targetActorID)
	if targetObjectURI.Valid {
		report.TargetObjectURI = targetObjectURI.String
	}
	return report, nil
}

func scanDomainBlock(row scanner) (DomainBlockResponse, error) {
	var block DomainBlockResponse
	if err := row.Scan(&block.ID, &block.Domain, &block.Severity, &block.Reason, &block.CreatedAt); err != nil {
		return DomainBlockResponse{}, err
	}
	return block, nil
}

func scanModerationAction(row scanner) (ModerationActionResponse, error) {
	var action ModerationActionResponse
	var reportID uuid.NullUUID
	if err := row.Scan(&action.ID, &action.ModeratorUserID, &action.TargetUserID, &action.TargetActorID,
		&action.Action, &action.Reason, &reportID, &action.CreatedAt); err != nil {
		return ModerationActionResponse{}, err
	}
	action.ReportID = dbx.UUIDPtr(reportID)
	return action, nil
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func normalizeReportState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "open", "reviewed", "resolved", "rejected":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeDomainBlockSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "silence", "suspend", "reject_media", "reject_all":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	value = strings.Trim(value, "/")
	if value == "" || strings.ContainsAny(value, "/ \t\r\n") {
		return ""
	}
	return value
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
