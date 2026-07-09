package observability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLoggerWritesJSONRequestLog(t *testing.T) {
	var logs bytes.Buffer
	handler := RequestLogger(&logs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/worlds", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %v, raw=%s", err, logs.String())
	}
	if entry["msg"] != "http_request" {
		t.Fatalf("msg = %v", entry["msg"])
	}
	if entry["method"] != "POST" {
		t.Fatalf("method = %v", entry["method"])
	}
	if entry["path"] != "/api/worlds" {
		t.Fatalf("path = %v", entry["path"])
	}
	if entry["status"].(float64) != http.StatusCreated {
		t.Fatalf("status = %v", entry["status"])
	}
}
