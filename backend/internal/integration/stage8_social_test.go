package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"basisvr-social-service/internal/api"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"github.com/google/uuid"
)

const stage8DID = "did:key:z6MkeTGwHmLmuCmgg4ABYhzWVh6ZX7hTwWt8gguAretUfc9c"

func stage8Setup(t *testing.T, options ...func(*config.Config)) (*sql.DB, http.Handler) {
	t.Helper()
	dsn := os.Getenv("BASIS_INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("BASIS_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := database.Open(ctx, config.DatabaseConfig{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.ApplyUp(ctx, db, migrationsPath(t)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.Database.URL = dsn
	cfg.Server.PublicURL = "https://social.integration.test"
	cfg.ActivityPub.Domain = "social.integration.test"
	cfg.ActivityPub.ActorKeyEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Auth.JWTSecret = "integration-test-secret-with-sufficient-length"
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Security.RateLimitEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	for _, configure := range options {
		configure(&cfg)
	}
	return db, api.NewRouter(api.Deps{DB: db, Config: cfg, Context: ctx})
}
func TestStage8FriendshipPrivacyAndPresence(t *testing.T) {
	db, r := stage8Setup(t)
	suffix := uuid.NewString()[:8]
	a := registerIntegrationUser(t, r, "s8a-"+suffix)
	b := registerIntegrationUser(t, r, "s8b-"+suffix)
	call := func(method, path, token string, body any, status int) []byte {
		return requestJSON(t, r, method, "/api/v1"+path, token, "", body, status)
	}
	checkSearch := func(token, query, state, direction string, friend, blocked, self bool) {
		t.Helper()
		body := call("GET", "/search/users?q="+query+"&limit=1", token, nil, 200)
		var page struct {
			Data []struct {
				Relationship struct {
					FriendState     string `json:"friendState"`
					FriendDirection string `json:"friendDirection"`
					Friend          bool   `json:"friend"`
					Blocked         bool   `json:"blocked"`
					Self            bool   `json:"self"`
				} `json:"relationship"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Data) != 1 {
			t.Fatal("search fixture missing")
		}
		v := page.Data[0].Relationship
		if v.FriendState != state || v.FriendDirection != direction || v.Friend != friend || v.Blocked != blocked || v.Self != self {
			t.Fatalf("wrong search relationship: %+v", v)
		}
	}
	checkSearch(a.AccessToken, "s8a-"+suffix, "none", "", false, false, true)
	refA := map[string]any{"actorId": a.User.ActorID}
	refB := map[string]any{"actorId": b.User.ActorID}
	call("POST", "/friends/request", a.AccessToken, refB, 200)
	checkSearch(a.AccessToken, "s8b-"+suffix, "pending", "outgoing", false, false, false)
	checkSearch(b.AccessToken, "s8a-"+suffix, "pending", "incoming", false, false, false)
	pending := call("GET", "/friends?state=pending&direction=incoming&limit=1", b.AccessToken, nil, 200)
	if !bytes.Contains(pending, []byte(a.User.ActorID.String())) {
		t.Fatal("pending request missing")
	}
	call("POST", "/friends/request", b.AccessToken, refA, 409)
	call("POST", "/friends/accept", b.AccessToken, refA, 200)
	checkSearch(a.AccessToken, "s8b-"+suffix, "accepted", "mutual", true, false, false)
	checkSearch("", "s8b-"+suffix, "none", "", false, false, false)
	call("POST", "/friends/request", a.AccessToken, refB, 200)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM relationships WHERE type='friend' AND state='accepted' AND actor_id IN ($1,$2)`, a.User.ActorID, b.User.ActorID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("repeat request downgraded friendship: count=%d err=%v", count, err)
	}
	call("POST", "/presence", a.AccessToken, map[string]any{"status": "online", "visibility": "friends", "showExactInstance": false}, 200)
	page := call("GET", "/presence/friends?limit=1", b.AccessToken, nil, 200)
	if !bytes.Contains(page, []byte(a.User.ActorID.String())) {
		t.Fatal("visible friend missing")
	}
	call("POST", "/presence", a.AccessToken, map[string]any{"status": "invisible", "visibility": "friends"}, 200)
	page = call("GET", "/presence/friends", b.AccessToken, nil, 200)
	if bytes.Contains(page, []byte(a.User.ActorID.String())) {
		t.Fatal("invisible friend leaked")
	}
	call("POST", "/presence", a.AccessToken, map[string]any{"worldId": uuid.New()}, 400)
	call("POST", "/presence", a.AccessToken, map[string]any{"expiresAt": time.Now().Add(time.Hour)}, 400)

	var world struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, call("POST", "/worlds", a.AccessToken, map[string]any{"name": "Invite filters", "slug": "s8-invites-" + suffix, "visibility": "public"}, 201), &world)
	var incoming, expired struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, call("POST", "/invites", b.AccessToken, map[string]any{"toActorId": a.User.ActorID, "worldId": world.ID}, 201), &incoming)
	decodeResponse(t, call("POST", "/invites", b.AccessToken, map[string]any{"toActorId": a.User.ActorID, "worldId": world.ID}, 201), &expired)
	if _, err := db.Exec(`UPDATE invites SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	call("POST", "/invites", a.AccessToken, map[string]any{"toActorId": b.User.ActorID, "worldId": world.ID}, 201)
	invitePage := call("GET", "/invites?direction=incoming&state=pending&limit=1", a.AccessToken, nil, 200)
	if !bytes.Contains(invitePage, []byte(incoming.ID.String())) || bytes.Contains(invitePage, []byte(expired.ID.String())) {
		t.Fatal("invitation filters not applied before pagination")
	}
	call("POST", "/invites/"+incoming.ID.String()+"/accept", a.AccessToken, nil, 200)
	invitePage = call("GET", "/invites?direction=incoming&state=accepted&limit=1", a.AccessToken, nil, 200)
	if !bytes.Contains(invitePage, []byte(incoming.ID.String())) {
		t.Fatal("accepted invitation missing")
	}
	call("GET", "/invites?direction=invalid", a.AccessToken, nil, 400)
	call("GET", "/invites?state=invalid", a.AccessToken, nil, 400)
	call("POST", "/users/"+a.User.ActorID.String()+"/block", b.AccessToken, nil, 200)
	checkSearch(a.AccessToken, "s8b-"+suffix, "none", "", false, true, false)
	call("POST", "/friends/request", a.AccessToken, refB, 403)
	call("POST", "/friends/accept", a.AccessToken, refB, 403)
	page = call("GET", "/friends", a.AccessToken, nil, 200)
	if bytes.Contains(page, []byte(b.User.ActorID.String())) {
		t.Fatal("blocked friend retained")
	}
	call("POST", "/users/"+a.User.ActorID.String()+"/unblock", b.AccessToken, nil, 204)
	call("POST", "/friends/request", a.AccessToken, refB, 200)
	call("POST", "/friends/accept", b.AccessToken, refA, 200)
	call("POST", "/friends/remove", a.AccessToken, refB, 204)
	page = call("GET", "/friends", b.AccessToken, nil, 200)
	if bytes.Contains(page, []byte(a.User.ActorID.String())) {
		t.Fatal("remove must be mutual")
	}
}
func TestStage8KeyBoundAdmissionAndCapacity(t *testing.T) {
	db, r := stage8Setup(t)
	suffix := uuid.NewString()[:8]
	owner := registerIntegrationUser(t, r, "s8host-"+suffix)
	a := registerIntegrationUser(t, r, "s8join-"+suffix)
	b := registerIntegrationUser(t, r, "s8race-"+suffix)
	if _, err := db.Exec(`UPDATE users SET role='admin' WHERE id=$1`, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, service string, body any, status int) []byte {
		return requestJSON(t, r, method, "/api/v1"+path, token, service, body, status)
	}
	var world struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, call("POST", "/worlds", owner.AccessToken, "", map[string]any{"slug": "s8-world-" + suffix, "name": "Stage8", "visibility": "public"}, 201), &world)
	var instance struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, call("POST", "/instances", owner.AccessToken, "", map[string]any{"worldId": world.ID, "capacity": 1, "visibility": "public"}, 201), &instance)
	type credential struct {
		ID    uuid.UUID `json:"id"`
		Token string    `json:"token"`
	}
	var server, wrong credential
	decodeResponse(t, call("POST", "/admin/world-server-credentials", owner.AccessToken, "", map[string]any{"name": "s8-server-" + suffix, "allowedWorldId": world.ID}, 201), &server)
	decodeResponse(t, call("POST", "/admin/world-server-credentials", owner.AccessToken, "", map[string]any{"name": "s8-wrong-" + suffix, "allowedWorldId": world.ID}, 201), &wrong)
	instancePath := "/instances/" + instance.ID.String()
	servicePath := "/service" + instancePath
	call("POST", servicePath+"/heartbeat", "", wrong.Token, map[string]any{}, 409)
	call("PUT", "/admin"+instancePath+"/world-server", a.AccessToken, "", map[string]any{"credentialId": server.ID}, 403)
	call("PUT", "/admin"+instancePath+"/world-server", owner.AccessToken, "", map[string]any{"credentialId": server.ID}, 204)
	call("POST", servicePath+"/heartbeat", "", server.Token, map[string]any{}, 200)
	call("POST", instancePath+"/join", a.AccessToken, "", map[string]any{}, 409)
	issue := func(token string) string {
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		decodeResponse(t, call("POST", instancePath+"/join-tickets", token, "", map[string]any{"clientDid": stage8DID, "presenceVisibility": "friends", "showExactInstance": true}, 201), &ticket)
		return ticket.Ticket
	}
	consume := func(ticket string, id uuid.UUID, did, service string, status int) {
		call("POST", "/service/instance-join-tickets/consume", "", service, map[string]any{"ticket": ticket, "instanceId": id, "clientDid": did}, status)
	}
	ticket := issue(a.AccessToken)
	consume(ticket, uuid.New(), stage8DID, server.Token, 409)
	consume(ticket, instance.ID, strings.TrimSuffix(stage8DID, "c")+"d", server.Token, 409)
	consume(ticket, instance.ID, stage8DID, wrong.Token, 409)
	if _, err := db.Exec(`UPDATE instance_join_tickets SET expires_at=now()-interval '1 second' WHERE actor_id=$1`, a.User.ActorID); err != nil {
		t.Fatal(err)
	}
	consume(ticket, instance.ID, stage8DID, server.Token, 409)
	ticket = issue(a.AccessToken)
	second := issue(b.AccessToken)
	// Both tickets are valid before capacity1 is contested. PostgreSQL row lock must admit exactly one.
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, raw := range []string{ticket, second} {
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{"ticket": raw, "instanceId": instance.ID, "clientDid": stage8DID})
			req := httptest.NewRequest("POST", "/api/v1/service/instance-join-tickets/consume", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Basis-Service-Token", server.Token)
			res := httptest.NewRecorder()
			r.ServeHTTP(res, req)
			statuses <- res.Code
		}(raw)
	}
	wg.Wait()
	close(statuses)
	var accepted, rejected int
	for status := range statuses {
		if status == 200 {
			accepted++
		} else if status == 409 {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent consume status%d", status)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("admitted%d rejected%d", accepted, rejected)
	}
	var actor uuid.UUID
	var winningTicket string
	if err := db.QueryRow(`SELECT actor_id FROM instance_members WHERE instance_id=$1 AND state='joined'`, instance.ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	winner := a
	if actor == b.User.ActorID {
		winner = b
		winningTicket = second
	} else {
		winningTicket = ticket
	}
	consume(winningTicket, instance.ID, stage8DID, server.Token, 409)
	call("PUT", "/admin"+instancePath+"/world-server", owner.AccessToken, "", map[string]any{"credentialId": wrong.ID}, 409)
	call("POST", instancePath+"/heartbeat", winner.AccessToken, "", map[string]any{}, 409)
	call("POST", "/presence", winner.AccessToken, "", map[string]any{"status": "invisible", "visibility": "nobody", "showExactInstance": false}, 200)
	call("POST", servicePath+"/members/"+actor.String()+"/heartbeat", "", server.Token, map[string]any{}, 200)
	var status, visibility string
	var exact bool
	var location uuid.UUID
	if err := db.QueryRow(`SELECT status,visibility,show_exact_instance,instance_id FROM presence_sessions WHERE actor_id=$1`, actor).Scan(&status, &visibility, &exact, &location); err != nil {
		t.Fatal(err)
	}
	if status != "invisible" || visibility != "nobody" || exact || location != instance.ID {
		t.Fatalf("heartbeat altered user privacy: %s %s %v", status, visibility, exact)
	}
	call("POST", "/users/"+actor.String()+"/block", owner.AccessToken, "", nil, 200)
	call("POST", servicePath+"/members/"+actor.String()+"/heartbeat", "", server.Token, map[string]any{}, 403)
	call("DELETE", servicePath+"/members/"+actor.String(), "", server.Token, nil, 204)
	call("POST", instancePath+"/join-tickets", winner.AccessToken, "", map[string]any{"clientDid": stage8DID}, 403)
	call("POST", "/invites", winner.AccessToken, "", map[string]any{"toActorId": owner.User.ActorID, "instanceId": instance.ID}, 403)
	var occupancy int
	if err := db.QueryRow(`SELECT current_users FROM instances WHERE id=$1`, instance.ID).Scan(&occupancy); err != nil || occupancy != 0 {
		t.Fatalf("capacity not released: %d %v", occupancy, err)
	}
}
