package activitypub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"basisvr-social-service/internal/activitypub/resolver"
	"basisvr-social-service/internal/activitypub/webfinger"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	resolver remoteActorResolver
}

const federationBlockedDomainExistsSQL = `
SELECT EXISTS (
  SELECT 1
  FROM domain_blocks
  WHERE lower(domain) = lower($1)
    AND severity IN ('suspend', 'reject_all')
)`

const inboxHTTPSignatureMaxSkew = 5 * time.Minute

func NewHandler(db *sql.DB, cfg config.Config) *Handler {
	return NewHandlerWithResolver(db, cfg, resolver.NewHTTPResolver(nil, cfg.ActivityPub.MaxRemoteResponseBytes))
}

type remoteActorResolver interface {
	ResolveActor(ctx context.Context, actorURI string) (resolver.RemoteActor, error)
}

func NewHandlerWithResolver(db *sql.DB, cfg config.Config, remoteResolver remoteActorResolver) *Handler {
	return &Handler{db: db, cfg: cfg, resolver: remoteResolver}
}

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/.well-known/webfinger", h.WebFinger)
	r.Post("/inbox", h.SharedInbox)
	r.Get("/users/{username}", h.PersonActor)
	r.Get("/users/{username}/followers", h.Followers)
	r.Get("/users/{username}/following", h.Following)
	r.Post("/users/{username}/inbox", h.Inbox)
	r.Get("/users/{username}/outbox", h.Outbox)
}

type localActorView struct {
	ID                uuid.UUID
	ActorURI          string
	Acct              string
	PreferredUsername string
	DisplayName       string
	Bio               string
	Domain            string
	InboxURL          string
	OutboxURL         string
	FollowersURL      string
	FollowingURL      string
	SharedInboxURL    string
	PublicKeyPEM      string
	AvatarURL         string
}

type localActorIdentity struct {
	ID       uuid.UUID
	ActorURI string
}

func (h *Handler) WebFinger(w http.ResponseWriter, r *http.Request) {
	resource := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("resource")))
	if !strings.HasPrefix(resource, "acct:") {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_resource", "resource must be an acct URI")
		return
	}

	acct := strings.TrimPrefix(resource, "acct:")
	if !h.isLocalAcct(acct) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "actor not found")
		return
	}

	var resolvedAcct string
	var actorURI string
	if err := h.db.QueryRowContext(r.Context(), `
SELECT a.acct, a.actor_uri
FROM actors a
JOIN users u ON u.id = a.local_user_id
WHERE a.is_local = true
  AND a.type = 'Person'
  AND lower(a.acct) = lower($1)
  AND u.status = 'active'`, acct).Scan(&resolvedAcct, &actorURI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "actor not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "webfinger_failed", err.Error())
		return
	}

	writeJRD(w, http.StatusOK, webfinger.BuildActorResource(resolvedAcct, actorURI))
}

func (h *Handler) PersonActor(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadActorByUsername(r, chi.URLParam(r, "username"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}

	doc := BuildPersonActorDocument(ActorMetadata{
		ActorURI:          actor.ActorURI,
		Acct:              actor.Acct,
		Type:              "Person",
		PreferredUsername: actor.PreferredUsername,
		DisplayName:       actor.DisplayName,
		Summary:           actor.Bio,
		Domain:            actor.Domain,
		InboxURL:          actor.InboxURL,
		OutboxURL:         actor.OutboxURL,
		FollowersURL:      actor.FollowersURL,
		FollowingURL:      actor.FollowingURL,
		SharedInboxURL:    actor.SharedInboxURL,
		PublicKeyPEM:      actor.PublicKeyPEM,
		IsLocal:           true,
		AvatarURL:         actor.AvatarURL,
	})

	writeActivityJSON(w, http.StatusOK, doc)
}

func (h *Handler) Followers(w http.ResponseWriter, r *http.Request) {
	h.collection(w, r, "followers", "incoming")
}

func (h *Handler) Following(w http.ResponseWriter, r *http.Request) {
	h.collection(w, r, "following", "outgoing")
}

func (h *Handler) Inbox(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadActorIdentityByUsername(r, chi.URLParam(r, "username"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	h.storeInboxMessage(w, r, actor)
}

func (h *Handler) SharedInbox(w http.ResponseWriter, r *http.Request) {
	// Shared inbox routing requires activity recipient resolution. Keep the
	// endpoint present for actor metadata, but leave processing to the inbox module.
	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted":        true,
		"processingState": "ignored",
	})
}

func (h *Handler) Outbox(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadActorIdentityByUsername(r, chi.URLParam(r, "username"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), `
SELECT COUNT(*)
FROM activities
WHERE actor_id = $1 AND direction IN ('local', 'outbound')`, actor.ID).Scan(&total); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "outbox_failed", err.Error())
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT raw_json
FROM activities
WHERE actor_id = $1 AND direction IN ('local', 'outbound')
ORDER BY published_at DESC
LIMIT 100`, actor.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "outbox_failed", err.Error())
		return
	}
	defer rows.Close()

	items := []any{}
	for rows.Next() {
		var rawJSON []byte
		if err := rows.Scan(&rawJSON); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "outbox_failed", err.Error())
			return
		}
		var item any
		if err := json.Unmarshal(rawJSON, &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "outbox_failed", err.Error())
		return
	}

	writeActivityJSON(w, http.StatusOK, orderedAnyCollection(actor.ActorURI+"/outbox", total, items))
}

func (h *Handler) collection(w http.ResponseWriter, r *http.Request, name string, direction string) {
	actor, err := h.loadActorIdentityByUsername(r, chi.URLParam(r, "username"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), `
SELECT COUNT(*)
FROM relationships rel
WHERE rel.actor_id = $1
  AND rel.type = 'follow'
  AND rel.state = 'accepted'
  AND rel.direction = '`+direction+`'`, actor.ID).Scan(&total); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "collection_failed", err.Error())
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT target.actor_uri
FROM relationships rel
JOIN actors target ON target.id = rel.target_actor_id
WHERE rel.actor_id = $1
  AND rel.type = 'follow'
  AND rel.state = 'accepted'
  AND rel.direction = '`+direction+`'
ORDER BY target.acct
LIMIT 500`, actor.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "collection_failed", err.Error())
		return
	}
	defer rows.Close()

	items := []string{}
	for rows.Next() {
		var actorURI string
		if err := rows.Scan(&actorURI); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "collection_failed", err.Error())
			return
		}
		items = append(items, actorURI)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "collection_failed", err.Error())
		return
	}

	writeActivityJSON(w, http.StatusOK, orderedCollection(actor.ActorURI+"/"+name, total, items))
}

func (h *Handler) storeInboxMessage(w http.ResponseWriter, r *http.Request, recipientActor localActorIdentity) {
	defer r.Body.Close()
	rawJSON, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	var activity map[string]any
	if err := json.Unmarshal(rawJSON, &activity); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	activityURI, _ := activity["id"].(string)
	if strings.TrimSpace(activityURI) == "" {
		activityURI = "urn:uuid:" + uuid.NewString()
	}
	activityType, _ := activity["type"].(string)
	if strings.TrimSpace(activityType) == "" {
		activityType = "Unknown"
	}
	if remoteActorURI := actorURIFromActivity(activity); remoteActorURI != "" {
		blocked, err := h.isFederationActorDomainBlocked(r.Context(), remoteActorURI)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "domain_block_check_failed", err.Error())
			return
		}
		if blocked {
			httpx.WriteError(w, http.StatusForbidden, "domain_blocked", "remote actor domain is blocked")
			return
		}
	}

	if requiresInboxHTTPSignature(activityType, activity) && strings.TrimSpace(r.Header.Get("Signature")) == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "missing_signature", "HTTP Signature is required")
		return
	}

	if activityType == "Follow" {
		h.processFollow(w, r, recipientActor, activityURI, activity, rawJSON)
		return
	}
	if activityType == "Undo" && undoType(activity) == "Follow" {
		h.processUndoFollow(w, r, recipientActor.ID, activityURI, activity, rawJSON)
		return
	}

	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO inbox_messages (recipient_actor_id, sender_actor_id, activity_uri, type, raw_json, signature_valid, processing_state)
VALUES ($1, NULL, $2, $3, $4, false, 'pending')`,
		recipientActor.ID,
		activityURI,
		activityType,
		rawJSON,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "inbox_store_failed", err.Error())
		return
	}

	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted":        true,
		"processingState": "pending",
	})
}

func (h *Handler) processFollow(w http.ResponseWriter, r *http.Request, recipientActor localActorIdentity, activityURI string, activity map[string]any, rawJSON []byte) {
	remoteActorURI := actorURIFromActivity(activity)
	if remoteActorURI == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_activity", "Follow.actor is required")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "follow_failed", err.Error())
		return
	}
	defer func() {
		_ = tx.Rollback()
	}()

	remoteActorID, remoteActor, err := h.ensureRemoteActor(r.Context(), tx, remoteActorURI)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "remote_actor_failed", err.Error())
		return
	}
	if err := h.verifyInboxHTTPSignature(r, rawJSON, remoteActor); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_signature", err.Error())
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO inbox_messages (recipient_actor_id, sender_actor_id, activity_uri, type, raw_json, signature_valid, processing_state)
VALUES ($1, $2, $3, 'Follow', $4, $5, 'processed')
ON CONFLICT DO NOTHING`,
		recipientActor.ID,
		remoteActorID,
		activityURI,
		rawJSON,
		true,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "inbox_store_failed", err.Error())
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO relationships (actor_id, target_actor_id, type, direction, state)
VALUES ($1, $2, 'follow', 'incoming', 'accepted')
ON CONFLICT (actor_id, target_actor_id, type)
DO UPDATE SET direction = 'incoming', state = 'accepted'`,
		recipientActor.ID,
		remoteActorID,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}

	acceptRaw := buildAcceptFollow(activityURI, recipientActor.ActorURI, remoteActor.ActorURI)
	acceptURI := h.publicURL() + "/activities/" + uuid.NewString()
	var activityID uuid.UUID
	if err := tx.QueryRowContext(r.Context(), `
INSERT INTO activities (activity_uri, actor_id, type, object_uri, object_type, visibility, raw_json, direction)
VALUES ($1, $2, 'Accept', $3, 'Follow', 'direct', $4, 'outbound')
RETURNING id`,
		acceptURI,
		recipientActor.ID,
		activityURI,
		acceptRaw,
	).Scan(&activityID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "accept_activity_failed", err.Error())
		return
	}

	targetInbox := remoteActor.InboxURL
	if remoteActor.SharedInbox != "" {
		targetInbox = remoteActor.SharedInbox
	}
	if targetInbox != "" {
		if _, err := tx.ExecContext(r.Context(), `
INSERT INTO outbox_jobs (activity_id, target_inbox_url)
VALUES ($1, $2)`,
			activityID,
			targetInbox,
		); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "outbox_job_failed", err.Error())
			return
		}
	}

	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "follow_failed", err.Error())
		return
	}

	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted":        true,
		"processingState": "processed",
		"activityUri":     acceptURI,
	})
}

func (h *Handler) processUndoFollow(w http.ResponseWriter, r *http.Request, recipientActorID uuid.UUID, activityURI string, activity map[string]any, rawJSON []byte) {
	remoteActorURI := actorURIFromActivity(activity)
	if remoteActorURI == "" {
		remoteActorURI = actorURIFromObject(activity["object"])
	}
	if remoteActorURI == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_activity", "Undo Follow actor is required")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "undo_follow_failed", err.Error())
		return
	}
	defer func() {
		_ = tx.Rollback()
	}()

	remoteActorID, remoteActor, err := h.ensureRemoteActor(r.Context(), tx, remoteActorURI)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "remote_actor_failed", err.Error())
		return
	}
	if err := h.verifyInboxHTTPSignature(r, rawJSON, remoteActor); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_signature", err.Error())
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO inbox_messages (recipient_actor_id, sender_actor_id, activity_uri, type, raw_json, signature_valid, processing_state)
VALUES ($1, $2, $3, 'Undo', $4, $5, 'processed')
ON CONFLICT DO NOTHING`,
		recipientActorID,
		remoteActorID,
		activityURI,
		rawJSON,
		true,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "inbox_store_failed", err.Error())
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
UPDATE relationships
SET state = 'removed'
WHERE actor_id = $1 AND target_actor_id = $2 AND type = 'follow'`,
		recipientActorID,
		remoteActorID,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}

	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "undo_follow_failed", err.Error())
		return
	}

	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted":        true,
		"processingState": "processed",
	})
}

func (h *Handler) ensureRemoteActor(ctx context.Context, tx *sql.Tx, actorURI string) (uuid.UUID, resolver.RemoteActor, error) {
	actorID, existingActor, err := h.loadRemoteActorTx(ctx, tx, actorURI)
	if err == nil {
		return actorID, existingActor, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, resolver.RemoteActor{}, err
	}

	remoteActor, err := h.resolver.ResolveActor(ctx, actorURI)
	if err != nil {
		return uuid.Nil, resolver.RemoteActor{}, err
	}
	if remoteActor.ActorURI == "" {
		remoteActor.ActorURI = actorURI
	}
	if remoteActor.Type == "" {
		remoteActor.Type = "Person"
	}
	if remoteActor.PreferredUsername == "" {
		remoteActor.PreferredUsername = preferredUsernameFromActorURI(remoteActor.ActorURI)
	}
	if remoteActor.Name == "" {
		remoteActor.Name = remoteActor.PreferredUsername
	}
	if remoteActor.Domain == "" {
		remoteActor.Domain = hostFromURL(remoteActor.ActorURI)
	}
	if remoteActor.Acct == "" {
		remoteActor.Acct = remoteActor.PreferredUsername + "@" + remoteActor.Domain
	}
	rawJSON := remoteActor.RawJSON
	if len(rawJSON) == 0 {
		rawJSON, _ = json.Marshal(map[string]any{
			"id":                remoteActor.ActorURI,
			"type":              remoteActor.Type,
			"preferredUsername": remoteActor.PreferredUsername,
			"name":              remoteActor.Name,
			"inbox":             remoteActor.InboxURL,
			"outbox":            remoteActor.OutboxURL,
			"followers":         remoteActor.FollowersURL,
			"following":         remoteActor.FollowingURL,
		})
	}

	var id uuid.UUID
	if err := tx.QueryRowContext(ctx, `
INSERT INTO actors (
  actor_uri, acct, type, preferred_username, display_name, domain,
  inbox_url, outbox_url, followers_url, following_url, shared_inbox_url,
  public_key_pem, is_local, raw_json, last_fetched_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, false, $13, now())
ON CONFLICT (actor_uri)
DO UPDATE SET acct = EXCLUDED.acct,
              type = EXCLUDED.type,
              preferred_username = EXCLUDED.preferred_username,
              display_name = EXCLUDED.display_name,
              domain = EXCLUDED.domain,
              inbox_url = EXCLUDED.inbox_url,
              outbox_url = EXCLUDED.outbox_url,
              followers_url = EXCLUDED.followers_url,
              following_url = EXCLUDED.following_url,
              shared_inbox_url = EXCLUDED.shared_inbox_url,
              public_key_pem = EXCLUDED.public_key_pem,
              raw_json = EXCLUDED.raw_json,
              last_fetched_at = now()
RETURNING id`,
		remoteActor.ActorURI,
		remoteActor.Acct,
		remoteActor.Type,
		remoteActor.PreferredUsername,
		remoteActor.Name,
		remoteActor.Domain,
		remoteActor.InboxURL,
		remoteActor.OutboxURL,
		remoteActor.FollowersURL,
		remoteActor.FollowingURL,
		nullString(remoteActor.SharedInbox),
		remoteActor.PublicKeyPEM,
		rawJSON,
	).Scan(&id); err != nil {
		return uuid.Nil, resolver.RemoteActor{}, err
	}

	return id, remoteActor, nil
}

func (h *Handler) loadRemoteActorTx(ctx context.Context, tx *sql.Tx, actorURI string) (uuid.UUID, resolver.RemoteActor, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, actor_uri, acct, type, preferred_username, display_name, domain,
       inbox_url, outbox_url, followers_url, following_url,
       COALESCE(shared_inbox_url, ''), public_key_pem
FROM actors
WHERE actor_uri = $1`, actorURI)
	var actorID uuid.UUID
	var actor resolver.RemoteActor
	err := row.Scan(
		&actorID,
		&actor.ActorURI,
		&actor.Acct,
		&actor.Type,
		&actor.PreferredUsername,
		&actor.Name,
		&actor.Domain,
		&actor.InboxURL,
		&actor.OutboxURL,
		&actor.FollowersURL,
		&actor.FollowingURL,
		&actor.SharedInbox,
		&actor.PublicKeyPEM,
	)
	return actorID, actor, err
}

func (h *Handler) findRemoteActorIDTx(ctx context.Context, tx *sql.Tx, actorURI string) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT id FROM actors WHERE actor_uri = $1`, actorURI).Scan(&id)
	return id, err
}

func (h *Handler) verifyInboxHTTPSignature(r *http.Request, body []byte, remoteActor resolver.RemoteActor) error {
	return VerifyHTTPSignatureRequest(r, body, remoteActor.PublicKeyPEM, remoteActor.ActorURI, time.Now(), inboxHTTPSignatureMaxSkew)
}

func (h *Handler) isFederationActorDomainBlocked(ctx context.Context, actorURI string) (bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(actorURI))
	if err != nil || parsed.Hostname() == "" {
		return false, nil
	}
	return h.isFederationDomainBlocked(ctx, parsed.Hostname())
}

func (h *Handler) isFederationDomainBlocked(ctx context.Context, domain string) (bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false, nil
	}
	var blocked bool
	err := h.db.QueryRowContext(ctx, federationBlockedDomainExistsSQL, domain).Scan(&blocked)
	return blocked, err
}

func (h *Handler) loadActorByUsername(r *http.Request, username string) (localActorView, error) {
	row := h.db.QueryRowContext(r.Context(), actorByUsernameSelect(), strings.ToLower(strings.TrimSpace(username)))
	var actor localActorView
	var sharedInbox sql.NullString
	if err := row.Scan(
		&actor.ID,
		&actor.ActorURI,
		&actor.Acct,
		&actor.PreferredUsername,
		&actor.DisplayName,
		&actor.Bio,
		&actor.Domain,
		&actor.InboxURL,
		&actor.OutboxURL,
		&actor.FollowersURL,
		&actor.FollowingURL,
		&sharedInbox,
		&actor.PublicKeyPEM,
		&actor.AvatarURL,
	); err != nil {
		return localActorView{}, err
	}
	if sharedInbox.Valid {
		actor.SharedInboxURL = sharedInbox.String
	}
	return actor, nil
}

func (h *Handler) loadActorIdentityByUsername(r *http.Request, username string) (localActorIdentity, error) {
	row := h.db.QueryRowContext(r.Context(), actorIDByUsernameSelect(), strings.ToLower(strings.TrimSpace(username)))
	var actor localActorIdentity
	if err := row.Scan(&actor.ID, &actor.ActorURI); err != nil {
		return localActorIdentity{}, err
	}
	return actor, nil
}

func (h *Handler) isLocalAcct(acct string) bool {
	parts := strings.Split(acct, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	domain := strings.ToLower(h.cfg.ActivityPub.Domain)
	if domain == "" {
		domain = strings.ToLower(hostFromURL(h.cfg.Server.PublicURL))
	}
	return strings.ToLower(parts[1]) == domain
}

func writeActorLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "actor not found")
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "actor_lookup_failed", err.Error())
}

func writeJRD(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/jrd+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeActivityJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", `application/activity+json; charset=utf-8`)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func orderedCollection(id string, total int, orderedItems []string) map[string]any {
	return map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           id,
		"type":         "OrderedCollection",
		"totalItems":   total,
		"orderedItems": orderedItems,
	}
}

func orderedAnyCollection(id string, total int, orderedItems []any) map[string]any {
	return map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           id,
		"type":         "OrderedCollection",
		"totalItems":   total,
		"orderedItems": orderedItems,
	}
}

func (h *Handler) publicURL() string {
	publicURL := strings.TrimRight(h.cfg.Server.PublicURL, "/")
	if publicURL == "" {
		return "http://localhost:8080"
	}
	return publicURL
}

func buildAcceptFollow(followURI string, localActorURI string, remoteActorURI string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Accept",
		"actor":    localActorURI,
		"to":       []string{remoteActorURI},
		"object":   followURI,
	})
	return raw
}

func actorURIFromActivity(activity map[string]any) string {
	switch value := activity["actor"].(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		return strings.TrimSpace(stringFromAny(value["id"]))
	default:
		return ""
	}
}

func actorURIFromObject(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		return actorURIFromActivity(typed)
	default:
		return ""
	}
}

func undoType(activity map[string]any) string {
	object, ok := activity["object"].(map[string]any)
	if !ok {
		return ""
	}
	return stringFromAny(object["type"])
}

func requiresInboxHTTPSignature(activityType string, activity map[string]any) bool {
	return activityType == "Follow" || (activityType == "Undo" && undoType(activity) == "Follow")
}

func stringFromAny(value any) string {
	stringValue, _ := value.(string)
	return stringValue
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func preferredUsernameFromActorURI(actorURI string) string {
	parsed, err := url.Parse(actorURI)
	if err != nil {
		return "remote"
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "remote"
	}
	return parts[len(parts)-1]
}

func actorByUsernameSelect() string {
	return `
SELECT a.id, a.actor_uri, a.acct, a.preferred_username,
       COALESCE(NULLIF(p.display_name, ''), a.display_name) AS display_name,
       p.bio, a.domain, a.inbox_url, a.outbox_url, a.followers_url, a.following_url,
       a.shared_inbox_url, a.public_key_pem, p.avatar_url
FROM actors a
JOIN users u ON u.id = a.local_user_id
JOIN profiles p ON p.user_id = u.id
WHERE a.is_local = true
  AND a.type = 'Person'
  AND lower(a.preferred_username) = lower($1)
  AND u.status = 'active'`
}

func actorIDByUsernameSelect() string {
	return `
SELECT a.id, a.actor_uri
FROM actors a
JOIN users u ON u.id = a.local_user_id
WHERE a.is_local = true
  AND a.type = 'Person'
  AND lower(a.preferred_username) = lower($1)
  AND u.status = 'active'`
}
