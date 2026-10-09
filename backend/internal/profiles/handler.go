package profiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/page"

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
	r.Get("/api/users/{username}", h.GetUser)
	r.Get("/api/search/users", h.SearchUsers)
	r.With(authMiddleware).Patch("/api/me/profile", h.UpdateMe)
}

type publicProfileResponse struct {
	ID           uuid.UUID        `json:"id"`
	ActorID      uuid.UUID        `json:"actorId"`
	Username     string           `json:"username"`
	Acct         string           `json:"acct"`
	DisplayName  string           `json:"displayName"`
	Bio          string           `json:"bio"`
	AvatarURL    string           `json:"avatarUrl"`
	BannerURL    string           `json:"bannerUrl"`
	StatusText   string           `json:"statusText"`
	Links        []any            `json:"links"`
	Relationship relationshipView `json:"relationship"`
}

type relationshipView struct {
	Self            bool   `json:"self"`
	FriendState     string `json:"friendState"`
	FriendDirection string `json:"friendDirection"`
	Following       bool   `json:"following"`
	FollowedBy      bool   `json:"followedBy"`
	Friend          bool   `json:"friend"`
	Blocked         bool   `json:"blocked"`
}

type updateProfileRequest struct {
	DisplayName     *string          `json:"displayName"`
	Bio             *string          `json:"bio"`
	AvatarURL       *string          `json:"avatarUrl"`
	BannerURL       *string          `json:"bannerUrl"`
	StatusText      *string          `json:"statusText"`
	Links           *json.RawMessage `json:"links"`
	PrivacySettings *json.RawMessage `json:"privacySettings"`
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	username := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "username")))
	if username == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_username", "username is required")
		return
	}

	profile, err := h.loadPublicProfile(r.Context(), username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_profile_failed", err.Error())
		return
	}
	profiles := []publicProfileResponse{profile}
	if err := h.enrichRelationships(r.Context(), profiles); err != nil {
		httpx.WriteError(w, 500, "load_relationships_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, profiles[0])
}

func (h *Handler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	requestPage, err := page.ParseTextRequest(r, 24, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		httpx.WriteJSON(w, http.StatusOK, page.Response[publicProfileResponse]{
			Data: []publicProfileResponse{}, Pagination: page.Metadata{Limit: requestPage.Limit},
		})
		return
	}
	var cursorText any
	cursorID := uuid.Nil
	if requestPage.Cursor != nil {
		cursorText = requestPage.Cursor.SortText
		cursorID = requestPage.Cursor.ID
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT u.id, a.id, u.username, a.acct, p.display_name, p.bio, p.avatar_url, p.banner_url, p.status_text, p.links
FROM users u
JOIN profiles p ON p.user_id = u.id
JOIN actors a ON a.local_user_id = u.id
WHERE u.status = 'active'
  AND (u.username ILIKE $1 OR p.display_name ILIKE $1 OR a.acct ILIKE $1)
  AND ($2::text IS NULL OR (lower(u.username), u.id) > ($2, $3))
ORDER BY lower(u.username), u.id
LIMIT $4`, "%"+query+"%", cursorText, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	defer rows.Close()

	results := []publicProfileResponse{}
	for rows.Next() {
		profile, err := scanPublicProfile(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		results = append(results, profile)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(results) > requestPage.Limit {
		last := results[requestPage.Limit-1]
		nextCursor = page.NextTextCursor(page.TextCursor{SortText: strings.ToLower(last.Username), ID: last.ID})
		results = results[:requestPage.Limit]
	}
	if err := rows.Close(); err != nil {
		httpx.WriteError(w, 500, "search_failed", err.Error())
		return
	}
	if err := h.enrichRelationships(r.Context(), results); err != nil {
		httpx.WriteError(w, 500, "load_relationships_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[publicProfileResponse]{
		Data: results, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req updateProfileRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	links, err := validateRawJSON(req.Links, []byte("[]"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_links", "links must be valid JSON")
		return
	}
	privacy, err := validateRawJSON(req.PrivacySettings, []byte("{}"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_privacy_settings", "privacySettings must be valid JSON")
		return
	}

	// Serialize profile changes with identity linking, which owns these canonical fields.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, 500, "update_profile_failed", "Unable to update profile.")
		return
	}
	defer tx.Rollback()
	var userID string
	if err = tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, principal.UserID).Scan(&userID); err != nil {
		httpx.WriteError(w, 500, "update_profile_failed", "Unable to update profile.")
		return
	}
	if req.DisplayName != nil || req.AvatarURL != nil {
		var managed bool
		if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM beeba_identity_links WHERE user_id=$1)`, principal.UserID).Scan(&managed); err != nil {
			httpx.WriteError(w, 500, "update_profile_failed", "Unable to update profile.")
			return
		}
		if managed {
			httpx.WriteError(w, 409, "managed_identity", "Change your display name and avatar in BeeBa.")
			return
		}
	}

	if _, err := tx.ExecContext(r.Context(), `
UPDATE profiles
SET
  display_name = COALESCE($2, display_name),
  bio = COALESCE($3, bio),
  avatar_url = COALESCE($4, avatar_url),
  banner_url = COALESCE($5, banner_url),
  status_text = COALESCE($6, status_text),
  links = COALESCE($7::jsonb, links),
  privacy_settings = COALESCE($8::jsonb, privacy_settings)
WHERE user_id = $1`,
		principal.UserID,
		dbx.NullString(req.DisplayName),
		dbx.NullString(req.Bio),
		dbx.NullString(req.AvatarURL),
		dbx.NullString(req.BannerURL),
		dbx.NullString(req.StatusText),
		links,
		privacy,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_profile_failed", err.Error())
		return
	}

	if err = tx.Commit(); err != nil {
		httpx.WriteError(w, 500, "update_profile_failed", "Unable to update profile.")
		return
	}

	profile, err := h.loadPublicProfile(r.Context(), principal.Username)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_profile_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, profile)
}

func (h *Handler) loadPublicProfile(ctx context.Context, username string) (publicProfileResponse, error) {
	row := h.db.QueryRowContext(ctx, `
SELECT u.id, a.id, u.username, a.acct, p.display_name, p.bio, p.avatar_url, p.banner_url, p.status_text, p.links
FROM users u
JOIN profiles p ON p.user_id = u.id
JOIN actors a ON a.local_user_id = u.id
WHERE u.username = $1 AND u.status = 'active'`, username)
	return scanPublicProfile(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPublicProfile(row scanner) (publicProfileResponse, error) {
	var profile publicProfileResponse
	var linksRaw []byte
	if err := row.Scan(
		&profile.ID,
		&profile.ActorID,
		&profile.Username,
		&profile.Acct,
		&profile.DisplayName,
		&profile.Bio,
		&profile.AvatarURL,
		&profile.BannerURL,
		&profile.StatusText,
		&linksRaw,
	); err != nil {
		return publicProfileResponse{}, err
	}
	profile.Links = dbx.DecodeJSON(linksRaw, []any{})
	profile.Relationship.FriendState = "none"
	return profile, nil
}

func validateRawJSON(raw *json.RawMessage, fallback []byte) (any, error) {
	if raw == nil {
		return nil, nil
	}
	if len(*raw) == 0 {
		return fallback, nil
	}
	var tmp any
	if err := json.Unmarshal(*raw, &tmp); err != nil {
		return nil, err
	}
	return []byte(*raw), nil
}

// Enrich only the displayed bounded page with one query. Do not issue a query per
// profile, and do not infer relationship state from public profile data.
func (h *Handler) enrichRelationships(ctx context.Context, profiles []publicProfileResponse) error {
	principal, authenticated := auth.PrincipalFromContext(ctx)
	ids := make([]string, 0, len(profiles))
	byActor := make(map[uuid.UUID]int, len(profiles))
	for i := range profiles {
		profiles[i].Relationship = relationshipView{FriendState: "none"}
		if !authenticated {
			continue
		}
		if profiles[i].ActorID == principal.ActorID {
			profiles[i].Relationship.Self = true
			continue
		}
		ids = append(ids, profiles[i].ActorID.String())
		byActor[profiles[i].ActorID] = i
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT CASE WHEN actor_id=$1 THEN target_actor_id ELSE actor_id END,type,direction,state,actor_id=$1 FROM relationships WHERE ((actor_id=$1 AND target_actor_id=ANY($2::uuid[])) OR (target_actor_id=$1 AND actor_id=ANY($2::uuid[]))) AND state IN ('accepted','pending')`, principal.ActorID, dbx.PostgresTextArray(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var actorID uuid.UUID
		var kind, direction, state string
		var outgoingRow bool
		if err := rows.Scan(&actorID, &kind, &direction, &state, &outgoingRow); err != nil {
			return err
		}
		index, ok := byActor[actorID]
		if !ok {
			continue
		}
		view := &profiles[index].Relationship
		if !outgoingRow {
			if direction == "outgoing" {
				direction = "incoming"
			} else if direction == "incoming" {
				direction = "outgoing"
			}
		}
		switch kind {
		case "block":
			view.Blocked = state == "accepted" || view.Blocked
		case "friend":
			if state == "accepted" {
				view.Friend = true
				view.FriendState = "accepted"
				view.FriendDirection = "mutual"
			} else if !view.Friend {
				view.FriendState = "pending"
				view.FriendDirection = direction
			}
		case "follow":
			if state == "accepted" {
				if direction == "outgoing" {
					view.Following = true
				} else if direction == "incoming" {
					view.FollowedBy = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range profiles {
		if profiles[i].Relationship.Blocked {
			profiles[i].Relationship = relationshipView{FriendState: "none", Blocked: true}
		}
	}
	return nil
}
