package events

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	activityoutbox "basisvr-social-service/internal/activitypub/outbox"
	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/page"
	"basisvr-social-service/internal/privacy"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db        *sql.DB
	publicURL string
	outbox    *activityoutbox.Service
}

func NewHandler(db *sql.DB, publicURL string) *Handler {
	publicURL = strings.TrimRight(publicURL, "/")
	return &Handler{db: db, publicURL: publicURL, outbox: activityoutbox.NewService(db, publicURL)}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Get("/api/events", h.List)
	r.Get("/api/events/{slug}", h.Get)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/events", h.Create)
		r.Patch("/api/events/{id}", h.Update)
		r.Delete("/api/events/{id}", h.Delete)
		r.Post("/api/events/{id}/rsvp", h.RSVP)
		r.Post("/api/events/{id}/announce", h.Announce)
	})
}

type createEventRequest struct {
	WorldID     uuid.UUID      `json:"worldId"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	StartTime   time.Time      `json:"startTime"`
	EndTime     time.Time      `json:"endTime"`
	Visibility  string         `json:"visibility"`
	LaunchURL   string         `json:"launchUrl"`
	Metadata    map[string]any `json:"metadata"`
}

type updateEventRequest struct {
	WorldID     *uuid.UUID      `json:"worldId"`
	Slug        *string         `json:"slug"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	StartTime   *time.Time      `json:"startTime"`
	EndTime     *time.Time      `json:"endTime"`
	Visibility  *string         `json:"visibility"`
	LaunchURL   *string         `json:"launchUrl"`
	Metadata    *map[string]any `json:"metadata"`
}

type rsvpRequest struct {
	State string `json:"state"`
}

type EventResponse struct {
	ID          uuid.UUID      `json:"id"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Owner       ownerResponse  `json:"owner"`
	World       worldResponse  `json:"world"`
	StartTime   time.Time      `json:"startTime"`
	EndTime     time.Time      `json:"endTime"`
	LaunchURL   string         `json:"launchUrl"`
	Visibility  string         `json:"visibility"`
	Metadata    map[string]any `json:"metadata"`
}

type ownerResponse struct {
	ActorID     uuid.UUID `json:"actorId"`
	Acct        string    `json:"acct"`
	DisplayName string    `json:"displayName"`
}

type worldResponse struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

const worldAccessTargetSQL = `SELECT owner_actor_id, visibility FROM worlds WHERE id = $1`
const eventAccessTargetSQL = `SELECT owner_actor_id, visibility FROM events WHERE id = $1`

const listEventsSQL = `
SELECT e.id, e.slug, e.name, e.description, e.start_time, e.end_time, e.launch_url, e.visibility, e.metadata,
       a.id, a.acct, a.display_name,
       w.id, w.slug, w.name
FROM events e
JOIN actors a ON a.id = e.owner_actor_id
LEFT JOIN users event_owner_user ON event_owner_user.id = a.local_user_id
JOIN worlds w ON w.id = e.world_id
JOIN actors world_owner ON world_owner.id = w.owner_actor_id
LEFT JOIN users world_owner_user ON world_owner_user.id = world_owner.local_user_id
WHERE e.visibility = 'public'
  AND (a.local_user_id IS NULL OR event_owner_user.status = 'active')
  AND (world_owner.local_user_id IS NULL OR world_owner_user.status = 'active')
  AND ($1::timestamptz IS NULL OR (e.start_time, e.id) > ($1, $2))
ORDER BY e.start_time ASC, e.id ASC
LIMIT $3`

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createEventRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	if req.WorldID == uuid.Nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_world", "worldId is required")
		return
	}
	if !req.EndTime.After(req.StartTime) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_time", "endTime must be after startTime")
		return
	}
	if req.Slug == "" {
		req.Slug = slugify(req.Name)
	} else {
		req.Slug = slugify(req.Slug)
	}
	if req.Visibility == "" {
		req.Visibility = "public"
	}
	allowed, err := h.canViewWorldByID(r.Context(), req.WorldID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeEventNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeEventNotFound(w)
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}

	var id uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `
INSERT INTO events (owner_actor_id, world_id, slug, name, description, start_time, end_time, visibility, launch_url, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id`,
		principal.ActorID,
		req.WorldID,
		req.Slug,
		req.Name,
		req.Description,
		req.StartTime,
		req.EndTime,
		req.Visibility,
		req.LaunchURL,
		metadata,
	).Scan(&id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_event_failed", err.Error())
		return
	}

	event, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_event_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, event)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	requestPage, err := page.ParseRequest(r, 24, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	var cursorTime any
	cursorID := uuid.Nil
	if requestPage.Cursor != nil {
		cursorTime = requestPage.Cursor.SortTime
		cursorID = requestPage.Cursor.ID
	}
	rows, err := h.db.QueryContext(r.Context(), listEventsSQL, cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_events_failed", err.Error())
		return
	}
	defer rows.Close()

	events := []EventResponse{}
	for rows.Next() {
		event, err := h.scanEvent(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_event_failed", err.Error())
			return
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_events_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(events) > requestPage.Limit {
		last := events[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.StartTime, ID: last.ID})
		events = events[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[EventResponse]{
		Data: events, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	event, err := h.loadBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeEventNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_event_failed", err.Error())
		return
	}
	allowed, err := h.canView(r.Context(), event.Owner.ActorID, event.Visibility)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "event_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeEventNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, event)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	_, id, ok := h.requireOwnerTarget(w, r)
	if !ok {
		return
	}

	var req updateEventRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	var slug *string
	if req.Slug != nil {
		value := slugify(*req.Slug)
		slug = &value
	}
	var metadata any
	if req.Metadata != nil {
		data, err := dbx.MarshalJSON(*req.Metadata, "{}")
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
			return
		}
		metadata = data
	}

	if _, err := h.db.ExecContext(r.Context(), `
UPDATE events
SET world_id = COALESCE($2, world_id),
    slug = COALESCE($3, slug),
    name = COALESCE($4, name),
    description = COALESCE($5, description),
    start_time = COALESCE($6, start_time),
    end_time = COALESCE($7, end_time),
    visibility = COALESCE($8, visibility),
    launch_url = COALESCE($9, launch_url),
    metadata = COALESCE($10::jsonb, metadata)
WHERE id = $1`,
		id,
		dbx.NullUUID(req.WorldID),
		dbx.NullString(slug),
		dbx.NullString(req.Name),
		dbx.NullString(req.Description),
		nullTime(req.StartTime),
		nullTime(req.EndTime),
		dbx.NullString(req.Visibility),
		dbx.NullString(req.LaunchURL),
		metadata,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_event_failed", err.Error())
		return
	}

	event, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_event_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, event)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	_, id, ok := h.requireOwnerTarget(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM events WHERE id = $1`, id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "delete_event_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RSVP(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	allowed, err := h.canViewEventByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeEventNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "event_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeEventNotFound(w)
		return
	}

	var req rsvpRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.State = strings.ToLower(strings.TrimSpace(req.State))
	if req.State == "" {
		req.State = "going"
	}
	if req.State != "going" && req.State != "interested" && req.State != "declined" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_state", "state must be going, interested or declined")
		return
	}

	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO event_rsvps (actor_id, event_id, state)
VALUES ($1, $2, $3)
ON CONFLICT (actor_id, event_id)
DO UPDATE SET state = EXCLUDED.state`, principal.ActorID, id, req.State); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "rsvp_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": req.State})
}

func (h *Handler) Announce(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	event, err := h.loadByID(r.Context(), id)
	if err != nil {
		writeEventNotFound(w)
		return
	}
	allowed, err := h.canView(r.Context(), event.Owner.ActorID, event.Visibility)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "event_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeEventNotFound(w)
		return
	}

	result, err := h.outbox.PublishAnnounce(r.Context(), activityoutbox.AnnounceInput{
		ActorID: principal.ActorID, ObjectID: event.ID, ObjectType: "Event",
		ObjectURI: h.publicURL + "/objects/" + event.ID.String(), Visibility: event.Visibility,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "announce_event_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"activityUri": result.ActivityURI, "federationDeliveries": result.Deliveries,
	})
}

func (h *Handler) requireOwnerTarget(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, uuid.Nil, false
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return auth.Principal{}, uuid.Nil, false
	}

	var ownerID uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `SELECT owner_actor_id FROM events WHERE id = $1`, id).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeEventNotFound(w)
			return auth.Principal{}, uuid.Nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_event_failed", err.Error())
		return auth.Principal{}, uuid.Nil, false
	}
	if ownerID != principal.ActorID {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only owner can modify this event")
		return auth.Principal{}, uuid.Nil, false
	}
	return principal, id, true
}

func (h *Handler) canViewWorldByID(ctx context.Context, id uuid.UUID) (bool, error) {
	var ownerID uuid.UUID
	var visibility string
	if err := h.db.QueryRowContext(ctx, worldAccessTargetSQL, id).Scan(&ownerID, &visibility); err != nil {
		return false, err
	}
	return h.canView(ctx, ownerID, visibility)
}

func (h *Handler) canViewEventByID(ctx context.Context, id uuid.UUID) (bool, error) {
	var ownerID uuid.UUID
	var visibility string
	if err := h.db.QueryRowContext(ctx, eventAccessTargetSQL, id).Scan(&ownerID, &visibility); err != nil {
		return false, err
	}
	return h.canView(ctx, ownerID, visibility)
}

func (h *Handler) canView(ctx context.Context, ownerID uuid.UUID, visibility string) (bool, error) {
	viewer := uuid.NullUUID{}
	if principal, ok := auth.PrincipalFromContext(ctx); ok {
		viewer = uuid.NullUUID{UUID: principal.ActorID, Valid: true}
	}
	return privacy.CanView(ctx, h.db, privacy.ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: viewer,
		Visibility:    visibility,
	})
}

func writeEventNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "event not found")
}

func (h *Handler) loadByID(ctx context.Context, id uuid.UUID) (EventResponse, error) {
	row := h.db.QueryRowContext(ctx, eventQuery()+` WHERE e.id = $1
  AND (a.local_user_id IS NULL OR event_owner_user.status = 'active')
  AND (world_owner.local_user_id IS NULL OR world_owner_user.status = 'active')`, id)
	return h.scanEvent(row)
}

func (h *Handler) loadBySlug(ctx context.Context, slug string) (EventResponse, error) {
	row := h.db.QueryRowContext(ctx, eventQuery()+` WHERE e.slug = $1
  AND (a.local_user_id IS NULL OR event_owner_user.status = 'active')
  AND (world_owner.local_user_id IS NULL OR world_owner_user.status = 'active')`, strings.ToLower(strings.TrimSpace(slug)))
	return h.scanEvent(row)
}

func eventQuery() string {
	return `
SELECT e.id, e.slug, e.name, e.description, e.start_time, e.end_time, e.launch_url, e.visibility, e.metadata,
       a.id, a.acct, a.display_name,
       w.id, w.slug, w.name
FROM events e
JOIN actors a ON a.id = e.owner_actor_id
LEFT JOIN users event_owner_user ON event_owner_user.id = a.local_user_id
JOIN worlds w ON w.id = e.world_id
JOIN actors world_owner ON world_owner.id = w.owner_actor_id
LEFT JOIN users world_owner_user ON world_owner_user.id = world_owner.local_user_id`
}

type scanner interface {
	Scan(dest ...any) error
}

func (h *Handler) scanEvent(row scanner) (EventResponse, error) {
	var event EventResponse
	var metadataRaw []byte
	if err := row.Scan(
		&event.ID,
		&event.Slug,
		&event.Name,
		&event.Description,
		&event.StartTime,
		&event.EndTime,
		&event.LaunchURL,
		&event.Visibility,
		&metadataRaw,
		&event.Owner.ActorID,
		&event.Owner.Acct,
		&event.Owner.DisplayName,
		&event.World.ID,
		&event.World.Slug,
		&event.World.Name,
	); err != nil {
		return EventResponse{}, err
	}
	event.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	return event, nil
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func nullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlug.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return uuid.NewString()
	}
	if len(value) > 80 {
		value = strings.Trim(value[:80], "-")
	}
	return value
}
