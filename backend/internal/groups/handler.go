package groups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"basisvr-social-service/internal/activitypub"
	activityoutbox "basisvr-social-service/internal/activitypub/outbox"
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/page"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type Handler struct {
	db                    *sql.DB
	publicURL             string
	actorKeyEncryptionKey string
	events                *realtime.Broker
	outbox                *activityoutbox.Service
}

func NewHandler(db *sql.DB, publicURL string, events *realtime.Broker, actorKeyEncryptionKey string) *Handler {
	publicURL = strings.TrimRight(publicURL, "/")
	return &Handler{actorKeyEncryptionKey: actorKeyEncryptionKey, db: db, publicURL: publicURL, events: events, outbox: activityoutbox.NewService(db, publicURL)}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Get("/api/groups", h.List)
	r.Get("/api/groups/{slug}", h.Get)
	r.Get("/api/groups/{id}/members", h.ListMembers)
	r.Get("/api/groups/{id}/worlds", h.ListWorlds)
	r.Get("/api/groups/{id}/events", h.ListEvents)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/groups", h.Create)
		r.Patch("/api/groups/{id}", h.Update)
		r.Post("/api/groups/{id}/join", h.Join)
		r.Post("/api/groups/{id}/leave", h.Leave)
		r.Patch("/api/groups/{id}/members/{actorId}", h.UpdateMember)
		r.Post("/api/groups/{id}/worlds", h.AddWorld)
		r.Delete("/api/groups/{id}/worlds/{worldId}", h.RemoveWorld)
		r.Post("/api/groups/{id}/events", h.AddEvent)
		r.Delete("/api/groups/{id}/events/{eventId}", h.RemoveEvent)
	})
}

type createRequest struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	AvatarURL   string         `json:"avatarUrl"`
	BannerURL   string         `json:"bannerUrl"`
	Visibility  string         `json:"visibility"`
	Metadata    map[string]any `json:"metadata"`
}

type updateRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	AvatarURL   *string         `json:"avatarUrl"`
	BannerURL   *string         `json:"bannerUrl"`
	Visibility  *string         `json:"visibility"`
	Metadata    *map[string]any `json:"metadata"`
}

type memberUpdateRequest struct {
	Role  *string `json:"role"`
	State *string `json:"state"`
}

type objectRequest struct {
	WorldID *uuid.UUID `json:"worldId"`
	EventID *uuid.UUID `json:"eventId"`
}

type GroupResponse struct {
	ID           uuid.UUID        `json:"id"`
	ActorID      uuid.UUID        `json:"actorId"`
	OwnerActorID *uuid.UUID       `json:"ownerActorId,omitempty"`
	Slug         string           `json:"slug"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	AvatarURL    string           `json:"avatarUrl"`
	BannerURL    string           `json:"bannerUrl"`
	Visibility   string           `json:"visibility"`
	Metadata     map[string]any   `json:"metadata"`
	MemberCount  int              `json:"memberCount"`
	Membership   *MemberResponse  `json:"membership,omitempty"`
	ActivityPub  ActivityPubLinks `json:"activityPub"`
	CreatedAt    time.Time        `json:"createdAt"`
}

type ActivityPubLinks struct {
	ActorURL     string `json:"actorUrl"`
	InboxURL     string `json:"inboxUrl"`
	OutboxURL    string `json:"outboxUrl"`
	FollowersURL string `json:"followersUrl"`
}

type MemberResponse struct {
	ActorID     uuid.UUID `json:"actorId"`
	Acct        string    `json:"acct"`
	DisplayName string    `json:"displayName"`
	AvatarURL   string    `json:"avatarUrl"`
	Role        string    `json:"role"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"createdAt"`
}

const groupSelect = `
SELECT g.id, g.actor_id, g.owner_actor_id, g.slug, g.name, g.description, g.avatar_url, g.banner_url,
       g.visibility, g.metadata, g.created_at,
       ga.actor_uri, ga.inbox_url, ga.outbox_url, ga.followers_url,
       (SELECT count(*) FROM group_members gm_count WHERE gm_count.group_id = g.id AND gm_count.state = 'active'),
       gm.actor_id, gm.role, gm.state, gm.created_at
FROM groups g
JOIN actors ga ON ga.id = g.actor_id
LEFT JOIN actors owner_actor ON owner_actor.id = g.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
LEFT JOIN group_members gm ON gm.group_id = g.id AND gm.actor_id = $1
WHERE (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')`

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	req.Slug = slugify(req.Slug)
	if req.Slug == "" {
		req.Slug = slugify(req.Name)
	}
	if req.Slug == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_slug", "slug is required")
		return
	}
	if req.Visibility == "" {
		req.Visibility = "public"
	}
	if !validVisibility(req.Visibility) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_visibility", "visibility must be public, private or invite_only")
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "group_key_failed", "could not generate actor key")
		return
	}
	meta := activitypub.BuildLocalGroupActorMetadata(activitypub.LocalActorInput{
		PublicURL: h.publicURL, Username: req.Slug, DisplayName: req.Name, Bio: req.Description,
		AvatarURL: req.AvatarURL, PublicKeyPEM: keyPair.PublicKeyPEM,
	})
	meta.PrivateKeyEncrypted, err = actorcrypto.Encrypt(h.actorKeyEncryptionKey, meta.ActorURI, keyPair.PrivateKeyPEM)
	if err != nil {
		httpx.WriteError(w, 500, "group_key_failed", "could not encrypt actor key")
		return
	}
	rawJSON, err := json.Marshal(activitypub.BuildGroupActorDocument(meta))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "group_actor_failed", err.Error())
		return
	}
	metadataJSON, _ := json.Marshal(req.Metadata)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_group_failed", err.Error())
		return
	}
	defer tx.Rollback()
	var groupActorID uuid.UUID
	if err := tx.QueryRowContext(r.Context(), `
INSERT INTO actors (actor_uri, acct, type, preferred_username, display_name, domain, inbox_url, outbox_url,
  followers_url, following_url, shared_inbox_url, public_key_pem, private_key_pem_encrypted, is_local, raw_json)
VALUES ($1,$2,'Group',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true,$13)
RETURNING id`, meta.ActorURI, meta.Acct, meta.PreferredUsername, meta.DisplayName, meta.Domain, meta.InboxURL,
		meta.OutboxURL, meta.FollowersURL, meta.FollowingURL, meta.SharedInboxURL, meta.PublicKeyPEM,
		meta.PrivateKeyEncrypted, rawJSON).Scan(&groupActorID); err != nil {
		writeCreateError(w, err)
		return
	}
	var groupID uuid.UUID
	if err := tx.QueryRowContext(r.Context(), `
INSERT INTO groups (actor_id, owner_actor_id, slug, name, description, avatar_url, banner_url, visibility, metadata)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING id`, groupActorID, principal.ActorID, req.Slug, req.Name, strings.TrimSpace(req.Description),
		strings.TrimSpace(req.AvatarURL), strings.TrimSpace(req.BannerURL), req.Visibility, metadataJSON).Scan(&groupID); err != nil {
		writeCreateError(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO group_members (group_id, actor_id, role, state) VALUES ($1,$2,'owner','active')`, groupID, principal.ActorID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_group_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_group_failed", err.Error())
		return
	}
	group, err := h.load(r.Context(), groupID.String(), uuid.NullUUID{UUID: principal.ActorID, Valid: true})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_group_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID}, "group.created", principal.ActorID, map[string]any{"groupId": groupID})
	httpx.WriteJSON(w, http.StatusCreated, group)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	viewer := viewerID(r)
	requestPage, err := page.ParseRequest(r, 24, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	var cursorTime any
	cursorID := uuid.Nil
	if requestPage.Cursor != nil {
		cursorTime, cursorID = requestPage.Cursor.SortTime, requestPage.Cursor.ID
	}
	rows, err := h.db.QueryContext(r.Context(), groupSelect+`
  AND (g.visibility = 'public' OR gm.state = 'active')
  AND ($2::timestamptz IS NULL OR (g.created_at, g.id) < ($2, $3))
ORDER BY g.created_at DESC, g.id DESC LIMIT $4`, viewerArg(viewer), cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_groups_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []GroupResponse{}
	for rows.Next() {
		item, err := scanGroup(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_group_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_groups_failed", err.Error())
		return
	}
	var nextCursor *string
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.CreatedAt, ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[GroupResponse]{Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit}})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	group, err := h.load(r.Context(), chi.URLParam(r, "slug"), viewerID(r))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, group)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	principal, groupID, ok := h.requireManager(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Visibility != nil && !validVisibility(*req.Visibility) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_visibility", "visibility must be public, private or invite_only")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_name", "name cannot be empty")
		return
	}
	var metadata any
	if req.Metadata != nil {
		metadata, _ = json.Marshal(*req.Metadata)
	}
	result, err := h.db.ExecContext(r.Context(), `
UPDATE groups SET
 name = COALESCE($2,name), description = COALESCE($3,description), avatar_url = COALESCE($4,avatar_url),
 banner_url = COALESCE($5,banner_url), visibility = COALESCE($6,visibility), metadata = COALESCE($7,metadata)
WHERE id = $1`, groupID, cleanPtr(req.Name), cleanPtr(req.Description), cleanPtr(req.AvatarURL), cleanPtr(req.BannerURL), req.Visibility, metadata)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_group_failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeLookupError(w, sql.ErrNoRows)
		return
	}
	group, err := h.load(r.Context(), groupID.String(), uuid.NullUUID{UUID: principal.ActorID, Valid: true})
	if err != nil {
		writeLookupError(w, err)
		return
	}
	realtime.PublishActorEvent(h.events, h.activeMemberIDs(r.Context(), groupID), "group.updated", principal.ActorID, map[string]any{"groupId": groupID})
	httpx.WriteJSON(w, http.StatusOK, group)
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	groupID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	var visibility string
	if err := h.db.QueryRowContext(r.Context(), `
SELECT g.visibility FROM groups g
JOIN actors owner_actor ON owner_actor.id = g.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE g.id = $1 AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')`, groupID).Scan(&visibility); err != nil {
		writeLookupError(w, err)
		return
	}
	state := "pending"
	if visibility == "public" {
		state = "active"
	}
	var member MemberResponse
	err = h.db.QueryRowContext(r.Context(), `
INSERT INTO group_members (group_id, actor_id, role, state) VALUES ($1,$2,'member',$3)
ON CONFLICT (group_id, actor_id) DO UPDATE SET state = CASE WHEN group_members.state = 'banned' THEN 'banned' ELSE EXCLUDED.state END
RETURNING actor_id, '', '', '', role, state, created_at`, groupID, principal.ActorID, state).Scan(
		&member.ActorID, &member.Acct, &member.DisplayName, &member.AvatarURL, &member.Role, &member.State, &member.CreatedAt)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_group_failed", err.Error())
		return
	}
	if member.State == "banned" {
		httpx.WriteError(w, http.StatusForbidden, "group_banned", "membership is banned")
		return
	}
	recipients := h.managerIDs(r.Context(), groupID)
	recipients = append(recipients, principal.ActorID)
	realtime.PublishActorEvent(h.events, recipients, "group.membership.updated", principal.ActorID, map[string]any{"groupId": groupID, "state": member.State})
	httpx.WriteJSON(w, http.StatusOK, member)
}

func (h *Handler) Leave(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	groupID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	result, err := h.db.ExecContext(r.Context(), `
DELETE FROM group_members
WHERE group_id = $1 AND actor_id = $2 AND role <> 'owner' AND state <> 'banned'`, groupID, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "leave_group_failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		httpx.WriteError(w, http.StatusConflict, "cannot_leave_group", "owner cannot leave or membership does not exist")
		return
	}
	realtime.PublishActorEvent(h.events, h.managerIDs(r.Context(), groupID), "group.member.left", principal.ActorID, map[string]any{"groupId": groupID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.load(r.Context(), groupID.String(), viewerID(r)); err != nil {
		writeLookupError(w, err)
		return
	}
	state := "active"
	if principal, exists := auth.PrincipalFromContext(r.Context()); exists && h.isManager(r.Context(), groupID, principal.ActorID) {
		if requested := strings.TrimSpace(r.URL.Query().Get("state")); requested != "" {
			state = requested
		}
	}
	if !validMemberState(state) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_state", "invalid member state")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT a.id, a.acct, a.display_name, COALESCE(p.avatar_url,''), gm.role, gm.state, gm.created_at
FROM group_members gm
JOIN actors a ON a.id = gm.actor_id
LEFT JOIN users u ON u.id = a.local_user_id
LEFT JOIN profiles p ON p.user_id = u.id
WHERE gm.group_id = $1 AND gm.state = $2 AND (a.local_user_id IS NULL OR u.status = 'active')
ORDER BY gm.created_at, a.id`, groupID, state)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_group_members_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []MemberResponse{}
	for rows.Next() {
		var item MemberResponse
		if err := rows.Scan(&item.ActorID, &item.Acct, &item.DisplayName, &item.AvatarURL, &item.Role, &item.State, &item.CreatedAt); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_group_member_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	principal, groupID, ok := h.requireManager(w, r)
	if !ok {
		return
	}
	targetID, ok := parseID(w, r, "actorId")
	if !ok {
		return
	}
	var req memberUpdateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Role != nil && (*req.Role != "moderator" && *req.Role != "member") {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_role", "role must be moderator or member")
		return
	}
	if req.State != nil && !validMemberState(*req.State) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_state", "state must be pending, active or banned")
		return
	}
	var callerRole, targetRole string
	if err := h.db.QueryRowContext(r.Context(), `
SELECT caller.role, target.role
FROM group_members caller
JOIN group_members target ON target.group_id = caller.group_id
WHERE caller.group_id = $1 AND caller.actor_id = $2 AND target.actor_id = $3`,
		groupID, principal.ActorID, targetID).Scan(&callerRole, &targetRole); err != nil {
		writeLookupError(w, err)
		return
	}
	if callerRole != "owner" && (targetRole != "member" || req.Role != nil) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only the owner can manage moderator roles")
		return
	}
	result, err := h.db.ExecContext(r.Context(), `
UPDATE group_members SET role = COALESCE($3,role), state = COALESCE($4,state)
WHERE group_id = $1 AND actor_id = $2 AND role <> 'owner'`, groupID, targetID, req.Role, req.State)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_group_member_failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeLookupError(w, sql.ErrNoRows)
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{targetID}, "group.membership.updated", principal.ActorID, map[string]any{"groupId": groupID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AddWorld(w http.ResponseWriter, r *http.Request)    { h.addObject(w, r, "world") }
func (h *Handler) AddEvent(w http.ResponseWriter, r *http.Request)    { h.addObject(w, r, "event") }
func (h *Handler) RemoveWorld(w http.ResponseWriter, r *http.Request) { h.removeObject(w, r, "world") }
func (h *Handler) RemoveEvent(w http.ResponseWriter, r *http.Request) { h.removeObject(w, r, "event") }
func (h *Handler) ListWorlds(w http.ResponseWriter, r *http.Request)  { h.listObjects(w, r, "world") }
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request)  { h.listObjects(w, r, "event") }

func (h *Handler) addObject(w http.ResponseWriter, r *http.Request, kind string) {
	principal, groupID, ok := h.requireManager(w, r)
	if !ok {
		return
	}
	var req objectRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, 400, "invalid_json", err.Error())
		return
	}
	objectID := req.WorldID
	if kind == "event" {
		objectID = req.EventID
	}
	if objectID == nil {
		httpx.WriteError(w, 400, "invalid_object", kind+"Id is required")
		return
	}
	table := "worlds"
	linkTable := "group_worlds"
	column := "world_id"
	if kind == "event" {
		table, linkTable, column = "events", "group_events", "event_id"
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	var objectVisibility string
	query := `SELECT visibility FROM ` + table + ` WHERE id = $1 AND owner_actor_id = $2`
	if err := tx.QueryRowContext(r.Context(), query, *objectID, principal.ActorID).Scan(&objectVisibility); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeLookupError(w, err)
		} else {
			httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		}
		return
	}
	var groupActorID uuid.UUID
	var groupVisibility string
	if err := tx.QueryRowContext(r.Context(), `SELECT actor_id, visibility FROM groups WHERE id = $1`, groupID).Scan(&groupActorID, &groupVisibility); err != nil {
		httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		return
	}
	query = `INSERT INTO ` + linkTable + ` (group_id, ` + column + `, added_by_actor_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`
	if _, err := tx.ExecContext(r.Context(), query, groupID, *objectID, principal.ActorID); err != nil {
		httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		return
	}
	visibility := "private"
	if groupVisibility == "public" && objectVisibility == "public" {
		visibility = "public"
	}
	objectType := "World"
	if kind == "event" {
		objectType = "Event"
	}
	if _, err := h.outbox.PublishAnnounceTx(r.Context(), tx, activityoutbox.AnnounceInput{
		ActorID: groupActorID, ObjectID: *objectID, ObjectType: objectType,
		ObjectURI: h.publicURL + "/objects/" + objectID.String(), Visibility: visibility,
	}); err != nil {
		httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, 500, "link_group_object_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, h.activeMemberIDs(r.Context(), groupID), "group."+kind+".added", principal.ActorID, map[string]any{"groupId": groupID, kind + "Id": *objectID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeObject(w http.ResponseWriter, r *http.Request, kind string) {
	principal, groupID, ok := h.requireManager(w, r)
	if !ok {
		return
	}
	param := "worldId"
	table := "group_worlds"
	column := "world_id"
	if kind == "event" {
		param, table, column = "eventId", "group_events", "event_id"
	}
	objectID, ok := parseID(w, r, param)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM `+table+` WHERE group_id = $1 AND `+column+` = $2`, groupID, objectID); err != nil {
		httpx.WriteError(w, 500, "unlink_group_object_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, h.activeMemberIDs(r.Context(), groupID), "group."+kind+".removed", principal.ActorID, map[string]any{"groupId": groupID, kind + "Id": objectID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request, kind string) {
	groupID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.load(r.Context(), groupID.String(), viewerID(r)); err != nil {
		writeLookupError(w, err)
		return
	}
	viewer := viewerID(r)
	query := `
SELECT w.id, w.slug, w.name
FROM group_worlds gw
JOIN worlds w ON w.id = gw.world_id
JOIN actors owner_actor ON owner_actor.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE gw.group_id = $1
  AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')
  AND (w.visibility = 'public' OR w.owner_actor_id = $2 OR ($2::uuid IS NOT NULL AND EXISTS (
    SELECT 1 FROM relationships rel
    WHERE rel.actor_id = w.owner_actor_id AND rel.target_actor_id = $2
      AND rel.state = 'accepted'
      AND ((w.visibility = 'followers' AND rel.type = 'follow') OR (w.visibility = 'friends' AND rel.type = 'friend'))
  )))
ORDER BY gw.created_at DESC`
	if kind == "event" {
		query = `
SELECT e.id, e.slug, e.name
FROM group_events ge
JOIN events e ON e.id = ge.event_id
JOIN actors owner_actor ON owner_actor.id = e.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE ge.group_id = $1
  AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')
  AND (e.visibility = 'public' OR e.owner_actor_id = $2 OR ($2::uuid IS NOT NULL AND EXISTS (
    SELECT 1 FROM relationships rel
    WHERE rel.actor_id = e.owner_actor_id AND rel.target_actor_id = $2
      AND rel.state = 'accepted'
      AND ((e.visibility = 'followers' AND rel.type = 'follow') OR (e.visibility = 'friends' AND rel.type = 'friend'))
  )))
ORDER BY ge.created_at DESC`
	}
	rows, err := h.db.QueryContext(r.Context(), query, groupID, viewerArg(viewer))
	if err != nil {
		httpx.WriteError(w, 500, "list_group_objects_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var slug, name string
		if err := rows.Scan(&id, &slug, &name); err != nil {
			httpx.WriteError(w, 500, "scan_group_object_failed", err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "slug": slug, "name": name})
	}
	httpx.WriteJSON(w, 200, items)
}

func (h *Handler) load(ctx context.Context, value string, viewer uuid.NullUUID) (GroupResponse, error) {
	query := groupSelect + ` AND (g.id::text = $2 OR lower(g.slug) = lower($2)) AND (g.visibility = 'public' OR gm.state = 'active')`
	return scanGroup(h.db.QueryRowContext(ctx, query, viewerArg(viewer), value))
}

func scanGroup(row interface{ Scan(...any) error }) (GroupResponse, error) {
	var item GroupResponse
	var owner uuid.NullUUID
	var metadata []byte
	var membershipActor uuid.NullUUID
	var role, state sql.NullString
	var memberCreated sql.NullTime
	if err := row.Scan(&item.ID, &item.ActorID, &owner, &item.Slug, &item.Name, &item.Description, &item.AvatarURL, &item.BannerURL,
		&item.Visibility, &metadata, &item.CreatedAt, &item.ActivityPub.ActorURL, &item.ActivityPub.InboxURL,
		&item.ActivityPub.OutboxURL, &item.ActivityPub.FollowersURL, &item.MemberCount, &membershipActor, &role, &state, &memberCreated); err != nil {
		return GroupResponse{}, err
	}
	if owner.Valid {
		item.OwnerActorID = &owner.UUID
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		item.Metadata = map[string]any{}
	}
	if role.Valid {
		item.Membership = &MemberResponse{ActorID: membershipActor.UUID, Role: role.String, State: state.String, CreatedAt: memberCreated.Time}
	}
	return item, nil
}

func (h *Handler) requireManager(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, 401, "unauthorized", "authentication required")
		return auth.Principal{}, uuid.Nil, false
	}
	groupID, ok := parseID(w, r, "id")
	if !ok {
		return auth.Principal{}, uuid.Nil, false
	}
	if !h.isManager(r.Context(), groupID, principal.ActorID) {
		httpx.WriteError(w, 403, "forbidden", "group manager role required")
		return auth.Principal{}, uuid.Nil, false
	}
	return principal, groupID, true
}

func (h *Handler) isManager(ctx context.Context, groupID, actorID uuid.UUID) bool {
	var allowed bool
	err := h.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id=$1 AND actor_id=$2 AND state='active' AND role IN ('owner','moderator'))`, groupID, actorID).Scan(&allowed)
	return err == nil && allowed
}

func (h *Handler) managerIDs(ctx context.Context, groupID uuid.UUID) []uuid.UUID {
	return h.memberIDs(ctx, groupID, true)
}
func (h *Handler) activeMemberIDs(ctx context.Context, groupID uuid.UUID) []uuid.UUID {
	return h.memberIDs(ctx, groupID, false)
}
func (h *Handler) memberIDs(ctx context.Context, groupID uuid.UUID, managersOnly bool) []uuid.UUID {
	query := `SELECT actor_id FROM group_members WHERE group_id=$1 AND state='active'`
	if managersOnly {
		query += ` AND role IN ('owner','moderator')`
	}
	rows, err := h.db.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func viewerID(r *http.Request) uuid.NullUUID {
	p, ok := auth.PrincipalFromContext(r.Context())
	return uuid.NullUUID{UUID: p.ActorID, Valid: ok}
}
func viewerArg(id uuid.NullUUID) any {
	if id.Valid {
		return id.UUID
	}
	return nil
}
func validVisibility(v string) bool  { return v == "public" || v == "private" || v == "invite_only" }
func validMemberState(v string) bool { return v == "pending" || v == "active" || v == "banned" }
func cleanPtr(v *string) any {
	if v == nil {
		return nil
	}
	return strings.TrimSpace(*v)
}
func parseID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, 400, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}
func writeLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, 404, "not_found", "group or object not found")
		return
	}
	httpx.WriteError(w, 500, "group_failed", err.Error())
}
func writeCreateError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		httpx.WriteError(w, 409, "group_exists", "group slug or actor handle already exists")
		return
	}
	httpx.WriteError(w, 500, "create_group_failed", err.Error())
}

var invalidSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func slugify(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = invalidSlug.ReplaceAllString(v, "-")
	return strings.Trim(v, "-._")
}
