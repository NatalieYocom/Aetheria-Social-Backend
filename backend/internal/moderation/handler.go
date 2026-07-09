package moderation

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db *sql.DB
}

func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/reports", h.CreateReport)
		r.Get("/api/me/reports", h.ListMyReports)

		r.Get("/api/moderation/reports", h.ListReports)
		r.Patch("/api/moderation/reports/{id}", h.UpdateReport)
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
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, reporter_actor_id, target_actor_id, target_object_uri, reason, state, created_at
FROM reports
WHERE reporter_actor_id = $1
ORDER BY created_at DESC
LIMIT $2`, principal.ActorID, parseLimit(r, 50, 100))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_reports_failed", err.Error())
		return
	}
	defer rows.Close()
	writeReports(w, rows)
}

func (h *Handler) ListReports(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	state := normalizeReportState(r.URL.Query().Get("state"))
	if state == "" {
		state = "open"
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT r.id, r.reporter_actor_id, r.target_actor_id, r.target_object_uri, r.reason, r.state, r.created_at
FROM reports r
WHERE r.state = $1
ORDER BY r.created_at DESC
LIMIT $2`, state, parseLimit(r, 50, 200))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_reports_failed", err.Error())
		return
	}
	defer rows.Close()
	writeReports(w, rows)
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

func (h *Handler) ListDomainBlocks(w http.ResponseWriter, r *http.Request) {
	if !h.requireModerator(w, r) {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, domain, severity, reason, created_at
FROM domain_blocks
ORDER BY domain ASC
LIMIT $1`, parseLimit(r, 100, 500))
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
	httpx.WriteJSON(w, http.StatusOK, items)
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
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}
	var role string
	if err := h.db.QueryRowContext(r.Context(), currentUserRoleSQL, principal.UserID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "moderator role required")
			return false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "role_check_failed", err.Error())
		return false
	}
	if role != "moderator" && role != "admin" {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "moderator role required")
		return false
	}
	return true
}

func writeReports(w http.ResponseWriter, rows *sql.Rows) {
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
	httpx.WriteJSON(w, http.StatusOK, items)
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

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func parseLimit(r *http.Request, fallback int, max int) int {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed > max {
		return max
	}
	return parsed
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
