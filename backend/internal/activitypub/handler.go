package activitypub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"basisvr-social-service/internal/activitypub/messagesig"
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
const activityJSONContentType = "application/activity+json; charset=utf-8"

var (
	errInvalidFederatedObject  = errors.New("invalid federated object")
	errObjectOwnershipMismatch = errors.New("federated object ownership mismatch")
)

type federatedObject struct {
	URI           string
	Type          string
	RawJSON       []byte
	PublishedAt   sql.NullTime
	SourceUpdated sql.NullTime
}

type localObjectView struct {
	Document     map[string]any
	Visibility   string
	OwnerActorID uuid.UUID
}

func NewHandler(db *sql.DB, cfg config.Config) *Handler {
	var client *http.Client
	if cfg.ActivityPub.Enabled {
		client = NewInstanceSignedClient(db, cfg, nil)
	}
	return NewHandlerWithResolver(db, cfg, resolver.NewHTTPResolver(client, cfg.ActivityPub.MaxRemoteResponseBytes))
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
	r.Get("/actor", h.ServiceActor)
	signedGet := r.With(h.requireAuthorizedFetch)
	signedGet.Get("/users/{username}", h.PersonActor)
	signedGet.Get("/users/{username}/followers", h.Followers)
	signedGet.Get("/users/{username}/following", h.Following)
	r.Post("/users/{username}/inbox", h.Inbox)
	signedGet.Get("/users/{username}/outbox", h.Outbox)
	signedGet.Get("/groups/{slug}", h.GroupActor)
	signedGet.Get("/groups/{slug}/followers", h.GroupFollowers)
	r.Post("/groups/{slug}/inbox", h.GroupInbox)
	signedGet.Get("/groups/{slug}/outbox", h.GroupOutbox)
	signedGet.Get("/objects/{id}", h.Object)
	signedGet.Get("/activities/{id}", h.Activity)
}

type verifiedActorContextKey struct{}

func (h *Handler) requireAuthorizedFetch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(strings.TrimSpace(h.cfg.ActivityPub.AuthorizedFetch), "all") {
			next.ServeHTTP(w, r)
			return
		}
		actor, verified, err := h.verifiedRequestActor(r, nil)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "authorized_fetch_failed", err.Error())
			return
		}
		if !verified {
			w.Header().Set("WWW-Authenticate", `Signature realm="ActivityPub"`)
			httpx.WriteError(w, http.StatusUnauthorized, "invalid_signature", "a valid HTTP Signature is required")
			return
		}
		ctx := context.WithValue(r.Context(), verifiedActorContextKey{}, actor)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
LEFT JOIN users u ON u.id = a.local_user_id
LEFT JOIN groups g ON g.actor_id = a.id
LEFT JOIN actors owner_actor ON owner_actor.id = g.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE a.is_local = true
  AND lower(a.acct) = lower($1)
  AND (
    (a.type = 'Person' AND u.status = 'active')
    OR (a.type = 'Group' AND g.id IS NOT NULL AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active'))
  )`, acct).Scan(&resolvedAcct, &actorURI); err != nil {
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

func (h *Handler) ServiceActor(w http.ResponseWriter, r *http.Request) {
	actorURI := h.publicURL() + "/actor"
	var actor localActorView
	err := h.db.QueryRowContext(r.Context(), `
SELECT actor_uri, acct, preferred_username, display_name, domain, inbox_url, outbox_url,
       followers_url, following_url, COALESCE(shared_inbox_url, ''), public_key_pem
FROM actors
WHERE actor_uri = $1 AND is_local = true AND type = 'Service'`, actorURI).Scan(
		&actor.ActorURI, &actor.Acct, &actor.PreferredUsername, &actor.DisplayName, &actor.Domain,
		&actor.InboxURL, &actor.OutboxURL, &actor.FollowersURL, &actor.FollowingURL,
		&actor.SharedInboxURL, &actor.PublicKeyPEM,
	)
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	writeActivityJSON(w, http.StatusOK, BuildServiceActorDocument(ActorMetadata{
		ActorURI: actor.ActorURI, Acct: actor.Acct, Type: "Service", PreferredUsername: actor.PreferredUsername,
		DisplayName: actor.DisplayName, Domain: actor.Domain, InboxURL: actor.InboxURL, OutboxURL: actor.OutboxURL,
		FollowersURL: actor.FollowersURL, FollowingURL: actor.FollowingURL, SharedInboxURL: actor.SharedInboxURL,
		PublicKeyPEM: actor.PublicKeyPEM, IsLocal: true,
	}))
}

func (h *Handler) GroupActor(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadGroupBySlug(r, chi.URLParam(r, "slug"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	writeActivityJSON(w, http.StatusOK, BuildGroupActorDocument(ActorMetadata{
		ActorURI: actor.ActorURI, Acct: actor.Acct, Type: "Group", PreferredUsername: actor.PreferredUsername,
		DisplayName: actor.DisplayName, Summary: actor.Bio, Domain: actor.Domain, InboxURL: actor.InboxURL,
		OutboxURL: actor.OutboxURL, FollowersURL: actor.FollowersURL, FollowingURL: actor.FollowingURL,
		SharedInboxURL: actor.SharedInboxURL, PublicKeyPEM: actor.PublicKeyPEM, IsLocal: true, AvatarURL: actor.AvatarURL,
	}))
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

func (h *Handler) GroupInbox(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadGroupIdentityBySlug(r, chi.URLParam(r, "slug"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	h.storeInboxMessage(w, r, actor)
}

func (h *Handler) SharedInbox(w http.ResponseWriter, r *http.Request) {
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
	recipients, err := h.loadSharedInboxRecipients(r.Context(), activityRecipientURIs(activity))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "recipient_lookup_failed", err.Error())
		return
	}
	if len(recipients) == 0 {
		writeActivityJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true, "processingState": "ignored", "recipients": 0,
		})
		return
	}
	for _, recipient := range recipients {
		cloned := r.Clone(r.Context())
		cloned.Body = io.NopCloser(bytes.NewReader(rawJSON))
		captured := newCapturedResponse()
		h.storeInboxMessage(captured, cloned, recipient)
		if captured.status >= http.StatusBadRequest {
			for key, values := range captured.header {
				w.Header()[key] = append([]string(nil), values...)
			}
			w.WriteHeader(captured.status)
			_, _ = w.Write(captured.body.Bytes())
			return
		}
	}
	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "processingState": "processed", "recipients": len(recipients),
	})
}

type capturedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newCapturedResponse() *capturedResponse {
	return &capturedResponse{header: make(http.Header), status: http.StatusOK}
}

func (w *capturedResponse) Header() http.Header { return w.header }

func (w *capturedResponse) WriteHeader(status int) { w.status = status }

func (w *capturedResponse) Write(data []byte) (int, error) { return w.body.Write(data) }

func (h *Handler) loadSharedInboxRecipients(ctx context.Context, recipientURIs []string) ([]localActorIdentity, error) {
	if len(recipientURIs) == 0 {
		return []localActorIdentity{}, nil
	}
	if len(recipientURIs) > 100 {
		recipientURIs = recipientURIs[:100]
	}
	placeholders := make([]string, len(recipientURIs))
	args := make([]any, len(recipientURIs))
	for index, uri := range recipientURIs {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
		args[index] = uri
	}
	in := strings.Join(placeholders, ",")
	rows, err := h.db.QueryContext(ctx, `
SELECT id, actor_uri
FROM actors
WHERE is_local = true
  AND (
    actor_uri IN (`+in+`) OR inbox_url IN (`+in+`) OR
    followers_url IN (`+in+`) OR following_url IN (`+in+`)
  )
ORDER BY actor_uri`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recipients := []localActorIdentity{}
	for rows.Next() {
		var recipient localActorIdentity
		if err := rows.Scan(&recipient.ID, &recipient.ActorURI); err != nil {
			return nil, err
		}
		recipients = append(recipients, recipient)
	}
	return recipients, rows.Err()
}

func activityRecipientURIs(activity map[string]any) []string {
	seen := map[string]struct{}{}
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case string:
			uri := strings.TrimSpace(typed)
			if uri != "" && uri != "https://www.w3.org/ns/activitystreams#Public" {
				seen[uri] = struct{}{}
			}
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			collect(typed["id"])
		}
	}
	for _, field := range []string{"to", "cc", "audience"} {
		collect(activity[field])
	}
	if object, ok := activity["object"].(map[string]any); ok {
		for _, field := range []string{"to", "cc", "audience"} {
			collect(object[field])
		}
	}
	result := make([]string, 0, len(seen))
	for uri := range seen {
		result = append(result, uri)
	}
	sort.Strings(result)
	return result
}

func (h *Handler) Activity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_activity_id", "activity id must be a UUID")
		return
	}
	var rawJSON []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT raw_json FROM activities WHERE id = $1`, id).Scan(&rawJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "activity_not_found", "activity not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "activity_failed", err.Error())
		return
	}
	var activity any
	if err := json.Unmarshal(rawJSON, &activity); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "activity_invalid", "stored activity is invalid")
		return
	}
	writeActivityJSON(w, http.StatusOK, activity)
}

func (h *Handler) Object(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_object_id", "object id must be a UUID")
		return
	}
	var objectURI, objectType string
	var rawJSON []byte
	var deleted bool
	if err := h.db.QueryRowContext(r.Context(), `
SELECT object_uri, type, raw_json, is_deleted
FROM activitypub_objects
WHERE id = $1`, id).Scan(&objectURI, &objectType, &rawJSON, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			object, localErr := h.loadLocalObject(r.Context(), id)
			if errors.Is(localErr, sql.ErrNoRows) {
				httpx.WriteError(w, http.StatusNotFound, "object_not_found", "object not found")
				return
			}
			if localErr != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "object_failed", localErr.Error())
				return
			}
			allowed, authErr := h.canFetchLocalObject(r, object)
			if authErr != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "object_authorization_failed", authErr.Error())
				return
			}
			if !allowed {
				httpx.WriteError(w, http.StatusNotFound, "object_not_found", "object not found")
				return
			}
			writeActivityJSON(w, http.StatusOK, object.Document)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "object_failed", err.Error())
		return
	}
	if deleted {
		writeActivityJSON(w, http.StatusOK, map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       objectURI, "type": "Tombstone", "formerType": objectType,
		})
		return
	}
	var object any
	if err := json.Unmarshal(rawJSON, &object); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "object_invalid", "stored object is invalid")
		return
	}
	objectMap, ok := object.(map[string]any)
	if !ok || activityVisibility(objectMap) != "public" {
		httpx.WriteError(w, http.StatusNotFound, "object_not_found", "object not found")
		return
	}
	writeActivityJSON(w, http.StatusOK, object)
}

func (h *Handler) loadLocalObject(ctx context.Context, id uuid.UUID) (localObjectView, error) {
	var name, description, previewURL, launchURL, actorURI string
	var visibility string
	var ownerActorID uuid.UUID
	var capacity int
	var createdAt, updatedAt time.Time
	err := h.db.QueryRowContext(ctx, `
SELECT w.name, w.description, w.preview_url, w.launch_url, w.capacity,
       actor.id, actor.actor_uri, w.visibility, w.created_at, w.updated_at
FROM worlds w
JOIN actors actor ON actor.id = w.owner_actor_id
	LEFT JOIN users owner_user ON owner_user.id = actor.local_user_id
LEFT JOIN groups owner_group ON owner_group.actor_id = actor.id
LEFT JOIN actors group_owner_actor ON group_owner_actor.id = owner_group.owner_actor_id
LEFT JOIN users group_owner_user ON group_owner_user.id = group_owner_actor.local_user_id
WHERE w.id = $1 AND w.visibility IN ('public', 'followers')
  AND ((actor.type = 'Person' AND owner_user.status = 'active')
    OR (actor.type = 'Group' AND owner_group.id IS NOT NULL
      AND (group_owner_actor.local_user_id IS NULL OR group_owner_user.status = 'active')))`, id).Scan(
		&name, &description, &previewURL, &launchURL, &capacity, &ownerActorID, &actorURI, &visibility, &createdAt, &updatedAt,
	)
	if err == nil {
		object := localObjectBase(h.publicURL()+"/objects/"+id.String(), h.publicURL()+"/ns#", "Page", "World", actorURI, name, description, createdAt, updatedAt)
		object["url"] = launchURL
		object["basis:capacity"] = capacity
		if previewURL != "" {
			object["image"] = map[string]any{"type": "Image", "url": previewURL}
		}
		return localObjectView{Document: object, Visibility: visibility, OwnerActorID: ownerActorID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return localObjectView{}, err
	}

	var startTime, endTime time.Time
	err = h.db.QueryRowContext(ctx, `
SELECT event.name, event.description, event.launch_url, actor.id, actor.actor_uri, event.visibility,
       event.start_time, event.end_time, event.created_at, event.updated_at
FROM events event
JOIN actors actor ON actor.id = event.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = actor.local_user_id
LEFT JOIN groups owner_group ON owner_group.actor_id = actor.id
LEFT JOIN actors group_owner_actor ON group_owner_actor.id = owner_group.owner_actor_id
LEFT JOIN users group_owner_user ON group_owner_user.id = group_owner_actor.local_user_id
WHERE event.id = $1 AND event.visibility IN ('public', 'followers')
  AND ((actor.type = 'Person' AND owner_user.status = 'active')
    OR (actor.type = 'Group' AND owner_group.id IS NOT NULL
      AND (group_owner_actor.local_user_id IS NULL OR group_owner_user.status = 'active')))`, id).Scan(
		&name, &description, &launchURL, &ownerActorID, &actorURI, &visibility, &startTime, &endTime, &createdAt, &updatedAt,
	)
	if err != nil {
		return localObjectView{}, err
	}
	object := localObjectBase(h.publicURL()+"/objects/"+id.String(), h.publicURL()+"/ns#", "Event", "Event", actorURI, name, description, createdAt, updatedAt)
	object["url"] = launchURL
	object["startTime"] = startTime.UTC().Format(time.RFC3339)
	object["endTime"] = endTime.UTC().Format(time.RFC3339)
	return localObjectView{Document: object, Visibility: visibility, OwnerActorID: ownerActorID}, nil
}

func (h *Handler) canFetchLocalObject(r *http.Request, object localObjectView) (bool, error) {
	mode := strings.ToLower(strings.TrimSpace(h.cfg.ActivityPub.AuthorizedFetch))
	if mode == "" {
		mode = "protected"
	}
	if object.Visibility == "public" && mode != "all" {
		return true, nil
	}
	if object.Visibility != "public" && (object.Visibility != "followers" || mode == "disabled") {
		return false, nil
	}
	remoteActor, verified := r.Context().Value(verifiedActorContextKey{}).(resolver.RemoteActor)
	var err error
	if !verified {
		remoteActor, verified, err = h.verifiedRequestActor(r, nil)
	}
	if err != nil || !verified {
		return false, err
	}
	if object.Visibility == "public" {
		return true, nil
	}
	var follows bool
	err = h.db.QueryRowContext(r.Context(), `
SELECT EXISTS (
  SELECT 1
  FROM relationships rel
  JOIN actors remote_actor ON remote_actor.id = rel.target_actor_id
  WHERE rel.actor_id = $1
    AND remote_actor.actor_uri = $2
    AND rel.type = 'follow' AND rel.direction = 'incoming' AND rel.state = 'accepted'
)`, object.OwnerActorID, remoteActor.ActorURI).Scan(&follows)
	return follows, err
}

func (h *Handler) verifiedRequestActor(r *http.Request, body []byte) (resolver.RemoteActor, bool, error) {
	keyID, err := requestSignatureKeyID(r)
	if err != nil {
		return resolver.RemoteActor{}, false, nil
	}
	actorURI := keyID
	if index := strings.IndexByte(actorURI, '#'); index >= 0 {
		actorURI = actorURI[:index]
	}
	blocked, err := h.isFederationActorDomainBlocked(r.Context(), actorURI)
	if err != nil {
		return resolver.RemoteActor{}, false, err
	}
	if blocked {
		return resolver.RemoteActor{}, false, nil
	}
	remoteActor, cacheErr := h.loadCachedRemoteActor(r.Context(), actorURI)
	if cacheErr == nil {
		if err := VerifyHTTPSignatureRequest(r, body, remoteActor.PublicKeyPEM, remoteActor.ActorURI, time.Now(), inboxHTTPSignatureMaxSkew); err == nil {
			return remoteActor, true, nil
		}
	} else if !errors.Is(cacheErr, sql.ErrNoRows) {
		return resolver.RemoteActor{}, false, cacheErr
	}
	remoteActor, err = h.resolver.ResolveActor(r.Context(), actorURI)
	if err != nil {
		return resolver.RemoteActor{}, false, nil
	}
	if err := VerifyHTTPSignatureRequest(r, body, remoteActor.PublicKeyPEM, remoteActor.ActorURI, time.Now(), inboxHTTPSignatureMaxSkew); err != nil {
		return resolver.RemoteActor{}, false, nil
	}
	return remoteActor, true, nil
}

func (h *Handler) loadCachedRemoteActor(ctx context.Context, actorURI string) (resolver.RemoteActor, error) {
	var actor resolver.RemoteActor
	err := h.db.QueryRowContext(ctx, `
SELECT actor_uri, domain, public_key_pem
FROM actors
WHERE actor_uri = $1 AND is_local = false AND public_key_pem <> ''`, actorURI).
		Scan(&actor.ActorURI, &actor.Domain, &actor.PublicKeyPEM)
	return actor, err
}

func requestSignatureKeyID(r *http.Request) (string, error) {
	if strings.TrimSpace(r.Header.Get("Signature-Input")) != "" {
		return messagesig.KeyID(r)
	}
	params, err := parseHTTPSignatureHeader(r.Header.Get("Signature"))
	if err != nil {
		return "", err
	}
	keyID := strings.TrimSpace(params["keyId"])
	if keyID == "" {
		return "", errors.New("Signature keyId is required")
	}
	return keyID, nil
}

func localObjectBase(id, namespace, activityType, basisType, actorURI, name, summary string, publishedAt, updatedAt time.Time) map[string]any {
	return map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{"basis": namespace},
		},
		"id": id, "type": activityType, "basis:objectType": basisType,
		"attributedTo": actorURI, "name": name, "summary": summary,
		"published": publishedAt.UTC().Format(time.RFC3339),
		"updated":   updatedAt.UTC().Format(time.RFC3339),
	}
}

func (h *Handler) Outbox(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadActorIdentityByUsername(r, chi.URLParam(r, "username"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	h.writeOutbox(w, r, actor)
}

func (h *Handler) GroupOutbox(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadGroupIdentityBySlug(r, chi.URLParam(r, "slug"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	h.writeOutbox(w, r, actor)
}

func (h *Handler) writeOutbox(w http.ResponseWriter, r *http.Request, actor localActorIdentity) {
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
	h.writeCollection(w, r, actor, name, direction)
}

func (h *Handler) GroupFollowers(w http.ResponseWriter, r *http.Request) {
	actor, err := h.loadGroupIdentityBySlug(r, chi.URLParam(r, "slug"))
	if err != nil {
		writeActorLookupError(w, err)
		return
	}
	h.writeCollection(w, r, actor, "followers", "incoming")
}

func (h *Handler) writeCollection(w http.ResponseWriter, r *http.Request, actor localActorIdentity, name string, direction string) {
	var total int
	if err := h.db.QueryRowContext(r.Context(), `
SELECT COUNT(*)
FROM relationships rel
JOIN actors target ON target.id = rel.target_actor_id
LEFT JOIN users target_user ON target_user.id = target.local_user_id
WHERE rel.actor_id = $1
  AND rel.type = 'follow'
  AND rel.state = 'accepted'
  AND (target.local_user_id IS NULL OR target_user.status = 'active')
  AND rel.direction = '`+direction+`'`, actor.ID).Scan(&total); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "collection_failed", err.Error())
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT target.actor_uri
FROM relationships rel
JOIN actors target ON target.id = rel.target_actor_id
LEFT JOIN users target_user ON target_user.id = target.local_user_id
WHERE rel.actor_id = $1
  AND rel.type = 'follow'
  AND rel.state = 'accepted'
  AND (target.local_user_id IS NULL OR target_user.status = 'active')
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
	if processableInboxActivity(activityType, activity) {
		h.processFederatedActivity(w, r, recipientActor.ID, activityURI, activityType, activity, rawJSON)
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

func (h *Handler) processFederatedActivity(w http.ResponseWriter, r *http.Request, recipientActorID uuid.UUID, activityURI string, activityType string, activity map[string]any, rawJSON []byte) {
	remoteActorURI := actorURIFromActivity(activity)
	if remoteActorURI == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_activity", activityType+".actor is required")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "activity_failed", err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	remoteActorID, remoteActor, err := h.ensureRemoteActor(r.Context(), tx, remoteActorURI)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "remote_actor_failed", err.Error())
		return
	}
	if err := h.verifyInboxHTTPSignature(r, rawJSON, remoteActor); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_signature", err.Error())
		return
	}

	result, err := tx.ExecContext(r.Context(), `
INSERT INTO inbox_messages (
  recipient_actor_id, sender_actor_id, activity_uri, type, raw_json,
  signature_valid, processing_state, processed_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'processed', now())
ON CONFLICT (recipient_actor_id, activity_uri) DO NOTHING`,
		recipientActorID, remoteActorID, activityURI, activityType, rawJSON, true,
	)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "inbox_store_failed", err.Error())
		return
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "inbox_store_failed", err.Error())
		return
	}
	if inserted > 0 {
		if err := h.applyFederatedObject(r.Context(), tx, remoteActorID, remoteActorURI, activityType, activity); err != nil {
			switch {
			case errors.Is(err, errInvalidFederatedObject):
				httpx.WriteError(w, http.StatusBadRequest, "invalid_object", err.Error())
			case errors.Is(err, errObjectOwnershipMismatch):
				httpx.WriteError(w, http.StatusForbidden, "object_ownership_mismatch", err.Error())
			default:
				httpx.WriteError(w, http.StatusInternalServerError, "object_store_failed", err.Error())
			}
			return
		}
		objectURI, objectType := activityObjectMetadata(activity["object"])
		if _, err := tx.ExecContext(r.Context(), `
INSERT INTO activities (
  activity_uri, actor_id, type, object_uri, object_type,
  visibility, raw_json, direction, received_at
)
VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, 'inbound', now())
ON CONFLICT (activity_uri) DO NOTHING`,
			activityURI, remoteActorID, activityType, objectURI, objectType,
			activityVisibility(activity), rawJSON,
		); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "activity_store_failed", err.Error())
			return
		}
	}

	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "activity_failed", err.Error())
		return
	}
	writeActivityJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "processingState": "processed", "duplicate": inserted == 0,
	})
}

func (h *Handler) applyFederatedObject(ctx context.Context, tx *sql.Tx, actorID uuid.UUID, actorURI, activityType string, activity map[string]any) error {
	if activityType != "Create" && activityType != "Update" && activityType != "Delete" {
		return nil
	}
	object, err := federatedObjectFromActivity(activity, actorURI)
	if err != nil {
		return err
	}
	switch activityType {
	case "Create":
		_, err = tx.ExecContext(ctx, `
INSERT INTO activitypub_objects (
  object_uri, attributed_to_actor_id, type, raw_json, published_at, source_updated_at
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (object_uri) DO NOTHING`,
			object.URI, actorID, object.Type, object.RawJSON, object.PublishedAt, object.SourceUpdated,
		)
		return err
	case "Update":
		result, err := tx.ExecContext(ctx, `
UPDATE activitypub_objects
SET type = $3, raw_json = $4, source_updated_at = $5, received_at = now()
WHERE object_uri = $1 AND attributed_to_actor_id = $2 AND is_deleted = false`,
			object.URI, actorID, object.Type, object.RawJSON, object.SourceUpdated,
		)
		return requireOwnedObject(result, err)
	case "Delete":
		result, err := tx.ExecContext(ctx, `
UPDATE activitypub_objects
SET is_deleted = true, raw_json = $3, received_at = now()
WHERE object_uri = $1 AND attributed_to_actor_id = $2 AND is_deleted = false`,
			object.URI, actorID, object.RawJSON,
		)
		return requireOwnedObject(result, err)
	}
	return nil
}

func requireOwnedObject(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errObjectOwnershipMismatch
	}
	return nil
}

func federatedObjectFromActivity(activity map[string]any, actorURI string) (federatedObject, error) {
	activityType := strings.TrimSpace(stringFromAny(activity["type"]))
	value := activity["object"]
	if activityType == "Delete" {
		if objectURI, ok := value.(string); ok && strings.TrimSpace(objectURI) != "" {
			raw, _ := json.Marshal(map[string]any{"id": strings.TrimSpace(objectURI), "type": "Tombstone"})
			return federatedObject{URI: strings.TrimSpace(objectURI), Type: "Tombstone", RawJSON: raw}, nil
		}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return federatedObject{}, fmt.Errorf("%w: embedded object is required", errInvalidFederatedObject)
	}
	objectURI := strings.TrimSpace(stringFromAny(object["id"]))
	objectType := strings.TrimSpace(stringFromAny(object["type"]))
	if objectURI == "" || objectType == "" {
		return federatedObject{}, fmt.Errorf("%w: object id and type are required", errInvalidFederatedObject)
	}
	if attributedTo := attributedToURI(object["attributedTo"]); attributedTo != "" && attributedTo != actorURI {
		return federatedObject{}, errObjectOwnershipMismatch
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return federatedObject{}, fmt.Errorf("%w: %v", errInvalidFederatedObject, err)
	}
	return federatedObject{
		URI: objectURI, Type: objectType, RawJSON: raw,
		PublishedAt:   parseActivityTime(stringFromAny(object["published"])),
		SourceUpdated: parseActivityTime(stringFromAny(object["updated"])),
	}, nil
}

func attributedToURI(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		return strings.TrimSpace(stringFromAny(typed["id"]))
	case []any:
		if len(typed) > 0 {
			return attributedToURI(typed[0])
		}
	}
	return ""
}

func parseActivityTime(value string) sql.NullTime {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: parsed, Valid: true}
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

func (h *Handler) loadGroupBySlug(r *http.Request, slug string) (localActorView, error) {
	row := h.db.QueryRowContext(r.Context(), groupBySlugSelect(), strings.ToLower(strings.TrimSpace(slug)))
	var actor localActorView
	var sharedInbox sql.NullString
	if err := row.Scan(&actor.ID, &actor.ActorURI, &actor.Acct, &actor.PreferredUsername, &actor.DisplayName,
		&actor.Bio, &actor.Domain, &actor.InboxURL, &actor.OutboxURL, &actor.FollowersURL, &actor.FollowingURL,
		&sharedInbox, &actor.PublicKeyPEM, &actor.AvatarURL); err != nil {
		return localActorView{}, err
	}
	if sharedInbox.Valid {
		actor.SharedInboxURL = sharedInbox.String
	}
	return actor, nil
}

func (h *Handler) loadGroupIdentityBySlug(r *http.Request, slug string) (localActorIdentity, error) {
	var actor localActorIdentity
	err := h.db.QueryRowContext(r.Context(), groupIDBySlugSelect(), strings.ToLower(strings.TrimSpace(slug))).Scan(&actor.ID, &actor.ActorURI)
	return actor, err
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
	w.Header().Set("Content-Type", activityJSONContentType)
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
	return activityType == "Follow" || processableInboxActivity(activityType, activity)
}

func processableInboxActivity(activityType string, activity map[string]any) bool {
	switch activityType {
	case "Create", "Update", "Delete", "Announce", "Like", "Accept", "Reject":
		return true
	case "Undo":
		switch undoType(activity) {
		case "Follow", "Announce", "Like":
			return true
		}
	}
	return false
}

func activityObjectMetadata(value any) (string, string) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), ""
	case map[string]any:
		return strings.TrimSpace(stringFromAny(typed["id"])), strings.TrimSpace(stringFromAny(typed["type"]))
	default:
		return "", ""
	}
}

func activityVisibility(activity map[string]any) string {
	const publicAudience = "https://www.w3.org/ns/activitystreams#Public"
	for _, field := range []string{"to", "cc"} {
		switch value := activity[field].(type) {
		case string:
			if value == publicAudience {
				return "public"
			}
		case []any:
			for _, recipient := range value {
				if stringFromAny(recipient) == publicAudience {
					return "public"
				}
			}
		}
	}
	return "direct"
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

func groupBySlugSelect() string {
	return `
SELECT a.id, a.actor_uri, a.acct, a.preferred_username, g.name, g.description, a.domain,
       a.inbox_url, a.outbox_url, a.followers_url, a.following_url, a.shared_inbox_url,
       a.public_key_pem, g.avatar_url
FROM groups g
JOIN actors a ON a.id = g.actor_id
JOIN actors owner_actor ON owner_actor.id = g.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE a.is_local = true AND a.type = 'Group' AND lower(g.slug) = lower($1)
  AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')`
}

func groupIDBySlugSelect() string {
	return `
SELECT a.id, a.actor_uri
FROM groups g
JOIN actors a ON a.id = g.actor_id
JOIN actors owner_actor ON owner_actor.id = g.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = owner_actor.local_user_id
WHERE a.is_local = true AND a.type = 'Group' AND lower(g.slug) = lower($1)
  AND (owner_actor.local_user_id IS NULL OR owner_user.status = 'active')`
}
