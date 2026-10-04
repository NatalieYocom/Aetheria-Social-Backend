// Package community exposes a deliberately bounded BeeBa delegation surface.
// Authentication and server ownership are checked by BeeBa and the link middleware;
// all Social visibility and mutation rules remain in the existing domain handlers.
package community

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/instances"
	"basisvr-social-service/internal/invites"
	"basisvr-social-service/internal/presence"
	"basisvr-social-service/internal/profiles"
	"basisvr-social-service/internal/realtime"
	"basisvr-social-service/internal/relationships"
	"basisvr-social-service/internal/worlds"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func RegisterRoutes(r chi.Router, db *sql.DB, cfg config.Config, broker *realtime.Broker) {
	relations := relationships.NewHandler(db, broker)
	world := worlds.NewHandler(db, cfg.Server.PublicURL)
	instance := instances.NewHandler(db, broker)
	invite := invites.NewHandler(db, broker)
	online := presence.NewHandler(db, broker)
	profile := profiles.NewHandler(db)
	mapping := mappingHandler{db: db, issuer: cfg.BeeBa.PublicURL, world: world}
	r.Route("/api/internal/beeba/community", func(r chi.Router) {
		r.Use(auth.BeeBaCommunityMiddleware(db, cfg.BeeBa))
		r.Get("/friends", relations.Friends)
		r.Post("/friends/request", relations.FriendRequest)
		r.Post("/friends/accept", relations.FriendAccept)
		r.Post("/friends/reject", relations.FriendReject)
		r.Post("/friends/remove", relations.FriendRemove)
		r.Get("/search/users", profile.SearchUsers)
		r.Post("/users/{id}/block", relations.Block)
		r.Post("/users/{id}/unblock", relations.Unblock)
		r.Get("/worlds", world.List)
		r.Get("/owned-worlds", world.ListOwned)
		r.Get("/worlds/{slug}", world.Get)
		r.Get("/worlds/{id}/instances", instance.ListByWorld)
		r.Get("/instances/{id}", instance.Get)
		r.Get("/invites", invite.List)
		r.Post("/invites", invite.Create)
		r.Post("/invites/{id}/accept", invite.Accept)
		r.Post("/invites/{id}/decline", invite.Decline)
		r.Get("/presence/me", online.Me)
		r.Get("/presence/friends", online.Friends)
		r.Post("/presence", online.Upsert)
		r.Get("/server-worlds/{id}", mapping.Get)
		r.Put("/server-worlds/{id}", mapping.Put)
		r.Delete("/server-worlds/{id}", mapping.Delete)
	})
}

type mappingHandler struct {
	db     *sql.DB
	issuer string
	world  *worlds.Handler
}

func (h mappingHandler) target(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	owner, e := uuid.Parse(r.Header.Get("X-BeeBa-Server-Owner"))
	if err != nil || e != nil {
		httpx.WriteError(w, 400, "invalid_request", "Invalid server identity.")
		return uuid.Nil, uuid.Nil, false
	}
	return id, owner, true
}
func (h mappingHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, owner, ok := h.target(w, r)
	if !ok {
		return
	}
	var slug string
	err := h.db.QueryRowContext(r.Context(), `SELECT w.slug FROM beeba_server_world_links l JOIN worlds w ON w.id=l.world_id JOIN beeba_identity_links i ON i.issuer=l.issuer AND i.beeba_user_id=l.beeba_user_id JOIN actors a ON a.local_user_id=i.user_id AND a.id=w.owner_actor_id WHERE l.issuer=$1 AND l.beeba_server_id=$2 AND l.beeba_user_id=$3`, h.issuer, id, owner).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, 404, "not_found", "No world is linked to this server.")
		return
	}
	if err != nil {
		httpx.WriteError(w, 503, "social_unavailable", "World link is unavailable.")
		return
	}
	// Reuse the world's current visibility policy; even its slug is not exposed on denial.
	route := chi.NewRouteContext()
	route.URLParams.Add("slug", slug)
	h.world.Get(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route)))
}
func (h mappingHandler) Put(w http.ResponseWriter, r *http.Request) {
	id, owner, ok := h.target(w, r)
	if !ok {
		return
	}
	if owner.String() != r.Header.Get("X-BeeBa-User-ID") {
		httpx.WriteError(w, 403, "forbidden", "Server ownership required.")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	var body struct {
		WorldID uuid.UUID `json:"worldId"`
	}
	if httpx.DecodeJSON(r, &body) != nil || body.WorldID == uuid.Nil {
		httpx.WriteError(w, 400, "invalid_request", "Choose a world.")
		return
	}
	result, err := h.db.ExecContext(r.Context(), `INSERT INTO beeba_server_world_links(issuer,beeba_server_id,beeba_user_id,world_id) SELECT $1,$2,$3,w.id FROM worlds w WHERE w.id=$4 AND w.owner_actor_id=$5 ON CONFLICT(issuer,beeba_server_id) DO UPDATE SET beeba_user_id=EXCLUDED.beeba_user_id,world_id=EXCLUDED.world_id,updated_at=clock_timestamp()`, h.issuer, id, owner, body.WorldID, p.ActorID)
	if err != nil {
		httpx.WriteError(w, 503, "social_unavailable", "World link could not be saved.")
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		httpx.WriteError(w, 503, "social_unavailable", "World link could not be saved.")
		return
	}
	if rows != 1 {
		httpx.WriteError(w, 404, "not_found", "Owned world not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h mappingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, owner, ok := h.target(w, r)
	if !ok {
		return
	}
	if owner.String() != r.Header.Get("X-BeeBa-User-ID") {
		httpx.WriteError(w, 403, "forbidden", "Server ownership required.")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM beeba_server_world_links WHERE issuer=$1 AND beeba_server_id=$2`, h.issuer, id); err != nil {
		httpx.WriteError(w, 503, "social_unavailable", "World link could not be removed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
