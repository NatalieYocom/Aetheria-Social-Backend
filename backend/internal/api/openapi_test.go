package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddedOpenAPIContract(t *testing.T) {
	var document struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &document); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q", document.OpenAPI)
	}
	required := map[string][]string{
		"/api/v1/auth/login":                  {"post"},
		"/api/v1/me":                          {"get"},
		"/api/v1/worlds":                      {"get", "post"},
		"/api/v1/events":                      {"get", "post"},
		"/api/v1/instances/{id}/join-tickets": {"post"},
		"/api/v1/service/instance-join-tickets/consume":              {"post"},
		"/api/v1/service/instances/{id}/heartbeat":                   {"post"},
		"/api/v1/service/instances/{id}/members/{actorId}/heartbeat": {"post"},
		"/api/v1/assets/search":                                      {"get"},
		"/api/v1/worlds/{id}/assets":                                 {"get", "post"},
		"/api/v1/realtime/events":                                    {"get"},
		"/api/v1/moderation/reports":                                 {"get"},
		"/api/v1/moderation/domain-blocks":                           {"get", "post"},
		"/api/v1/worlds/{id}/instances":                              {"get"},
		"/api/v1/admin/world-server-credentials":                     {"get", "post"},
		"/api/v1/admin/instance-join-audit":                          {"get"},
		"/api/v1/moderation/users/{actorId}/actions":                 {"post"},
		"/api/v1/moderation/actions":                                 {"get"},
		"/api/v1/groups":                                             {"get", "post"},
		"/api/v1/groups/{id}/join":                                   {"post"},
		"/api/v1/groups/{id}/members/{actorId}":                      {"patch"},
		"/api/v1/groups/{id}/worlds":                                 {"get", "post"},
		"/api/v1/ws":                                                 {"get"},
	}
	for path, methods := range required {
		operations, ok := document.Paths[path]
		if !ok {
			t.Fatalf("required path %s is missing", path)
		}
		for _, method := range methods {
			if _, ok := operations[method]; !ok {
				t.Fatalf("required operation %s %s is missing", method, path)
			}
		}
	}
}

func TestOpenAPIEndpoint(t *testing.T) {
	res := httptest.NewRecorder()
	serveOpenAPI(res, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if len(res.Body.Bytes()) != len(openAPISpec) {
		t.Fatalf("body length = %d, want %d", len(res.Body.Bytes()), len(openAPISpec))
	}
}
