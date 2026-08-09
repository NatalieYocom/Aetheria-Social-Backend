package worlds

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
	r.Get("/api/worlds", h.List)
	r.Get("/api/worlds/{slug}", h.Get)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/worlds", h.Create)
		r.Patch("/api/worlds/{id}", h.Update)
		r.Delete("/api/worlds/{id}", h.Delete)
		r.Post("/api/worlds/{id}/favorite", h.Favorite)
		r.Post("/api/worlds/{id}/announce", h.Announce)
	})
}

type createWorldRequest struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PreviewURL  string         `json:"previewUrl"`
	LaunchURL   string         `json:"launchUrl"`
	Visibility  string         `json:"visibility"`
	Capacity    int            `json:"capacity"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata"`
}

type updateWorldRequest struct {
	Slug        *string         `json:"slug"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	PreviewURL  *string         `json:"previewUrl"`
	LaunchURL   *string         `json:"launchUrl"`
	Visibility  *string         `json:"visibility"`
	Capacity    *int            `json:"capacity"`
	Tags        *[]string       `json:"tags"`
	Metadata    *map[string]any `json:"metadata"`
}

type WorldResponse struct {
	ID          uuid.UUID        `json:"id"`
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Owner       ownerResponse    `json:"owner"`
	PreviewURL  string           `json:"previewUrl"`
	LaunchURL   string           `json:"launchUrl"`
	Visibility  string           `json:"visibility"`
	Capacity    int              `json:"capacity"`
	Tags        []string         `json:"tags"`
	Metadata    map[string]any   `json:"metadata"`
	ActivityPub activityPubLinks `json:"activityPub"`
}

type ownerResponse struct {
	ActorID     uuid.UUID `json:"actorId"`
	Acct        string    `json:"acct"`
	DisplayName string    `json:"displayName"`
}

type activityPubLinks struct {
	ObjectURL string `json:"objectUrl"`
}

const worldAccessTargetSQL = `SELECT owner_actor_id, visibility FROM worlds WHERE id = $1`

const listWorldsSQL = `
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name, w.created_at
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.visibility = 'public'
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')
  AND ($1::timestamptz IS NULL OR (w.created_at, w.id) < ($1, $2))
ORDER BY w.created_at DESC, w.id DESC
LIMIT $3`

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createWorldRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_name", "name is required")
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
	if req.Capacity < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_capacity", "capacity cannot be negative")
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}

	var id uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `
INSERT INTO worlds (owner_actor_id, slug, name, description, preview_url, launch_url, visibility, capacity, tags, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::text[], $10)
RETURNING id`,
		principal.ActorID,
		req.Slug,
		req.Name,
		req.Description,
		req.PreviewURL,
		req.LaunchURL,
		req.Visibility,
		req.Capacity,
		dbx.PostgresTextArray(req.Tags),
		metadata,
	).Scan(&id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_world_failed", err.Error())
		return
	}

	world, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_world_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, world)
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
	rows, err := h.db.QueryContext(r.Context(), listWorldsSQL, cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_worlds_failed", err.Error())
		return
	}
	defer rows.Close()

	type listedWorld struct {
		world     WorldResponse
		createdAt time.Time
	}
	listed := []listedWorld{}
	for rows.Next() {
		world, createdAt, err := h.scanListedWorld(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_world_failed", err.Error())
			return
		}
		listed = append(listed, listedWorld{world: world, createdAt: createdAt})
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_worlds_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(listed) > requestPage.Limit {
		last := listed[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.createdAt, ID: last.world.ID})
		listed = listed[:requestPage.Limit]
	}
	worlds := make([]WorldResponse, 0, len(listed))
	for _, item := range listed {
		worlds = append(worlds, item.world)
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[WorldResponse]{
		Data: worlds, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	world, err := h.loadBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeWorldNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_world_failed", err.Error())
		return
	}
	allowed, err := h.canView(r.Context(), world.Owner.ActorID, world.Visibility)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeWorldNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, world)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	principal, id, ok := h.requireOwnerTarget(w, r)
	if !ok {
		_ = principal
		return
	}

	var req updateWorldRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	var tags any
	if req.Tags != nil {
		tags = dbx.PostgresTextArray(*req.Tags)
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
	var slug *string
	if req.Slug != nil {
		value := slugify(*req.Slug)
		slug = &value
	}

	if _, err := h.db.ExecContext(r.Context(), `
UPDATE worlds
SET slug = COALESCE($2, slug),
    name = COALESCE($3, name),
    description = COALESCE($4, description),
    preview_url = COALESCE($5, preview_url),
    launch_url = COALESCE($6, launch_url),
    visibility = COALESCE($7, visibility),
    capacity = COALESCE($8, capacity),
    tags = COALESCE($9::text[], tags),
    metadata = COALESCE($10::jsonb, metadata)
WHERE id = $1`,
		id,
		dbx.NullString(slug),
		dbx.NullString(req.Name),
		dbx.NullString(req.Description),
		dbx.NullString(req.PreviewURL),
		dbx.NullString(req.LaunchURL),
		dbx.NullString(req.Visibility),
		nullInt(req.Capacity),
		tags,
		metadata,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_world_failed", err.Error())
		return
	}

	world, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_world_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, world)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	_, id, ok := h.requireOwnerTarget(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM worlds WHERE id = $1`, id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "delete_world_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Favorite(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	allowed, err := h.canViewWorldByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeWorldNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeWorldNotFound(w)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO world_favorites (actor_id, world_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING`, principal.ActorID, id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "favorite_world_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"favorited": true})
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
	world, err := h.loadByID(r.Context(), id)
	if err != nil {
		writeWorldNotFound(w)
		return
	}
	allowed, err := h.canView(r.Context(), world.Owner.ActorID, world.Visibility)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeWorldNotFound(w)
		return
	}

	result, err := h.outbox.PublishAnnounce(r.Context(), activityoutbox.AnnounceInput{
		ActorID: principal.ActorID, ObjectID: world.ID, ObjectType: "World",
		ObjectURI: h.publicURL + "/objects/" + world.ID.String(), Visibility: world.Visibility,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "announce_world_failed", err.Error())
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
	if err := h.db.QueryRowContext(r.Context(), `SELECT owner_actor_id FROM worlds WHERE id = $1`, id).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeWorldNotFound(w)
			return auth.Principal{}, uuid.Nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_world_failed", err.Error())
		return auth.Principal{}, uuid.Nil, false
	}
	if ownerID != principal.ActorID {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only owner can modify this world")
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

func writeWorldNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "world not found")
}

func (h *Handler) loadByID(ctx context.Context, id uuid.UUID) (WorldResponse, error) {
	row := h.db.QueryRowContext(ctx, `
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.id = $1
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')`, id)
	return h.scanWorld(row)
}

func (h *Handler) loadBySlug(ctx context.Context, slug string) (WorldResponse, error) {
	row := h.db.QueryRowContext(ctx, `
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.slug = $1
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')`, strings.ToLower(strings.TrimSpace(slug)))
	return h.scanWorld(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func (h *Handler) scanWorld(row scanner) (WorldResponse, error) {
	var world WorldResponse
	var tags dbx.TextArray
	var metadataRaw []byte
	if err := row.Scan(
		&world.ID,
		&world.Slug,
		&world.Name,
		&world.Description,
		&world.PreviewURL,
		&world.LaunchURL,
		&world.Visibility,
		&world.Capacity,
		&tags,
		&metadataRaw,
		&world.Owner.ActorID,
		&world.Owner.Acct,
		&world.Owner.DisplayName,
	); err != nil {
		return WorldResponse{}, err
	}
	world.Tags = []string(tags)
	world.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	world.ActivityPub.ObjectURL = h.publicURL + "/objects/" + world.ID.String()
	return world, nil
}

func (h *Handler) scanListedWorld(row scanner) (WorldResponse, time.Time, error) {
	var world WorldResponse
	var tags dbx.TextArray
	var metadataRaw []byte
	var createdAt time.Time
	if err := row.Scan(
		&world.ID, &world.Slug, &world.Name, &world.Description, &world.PreviewURL,
		&world.LaunchURL, &world.Visibility, &world.Capacity, &tags, &metadataRaw,
		&world.Owner.ActorID, &world.Owner.Acct, &world.Owner.DisplayName, &createdAt,
	); err != nil {
		return WorldResponse{}, time.Time{}, err
	}
	world.Tags = []string(tags)
	world.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	world.ActivityPub.ObjectURL = h.publicURL + "/objects/" + world.ID.String()
	return world, createdAt, nil
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func nullInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
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
