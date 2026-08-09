package api

import (
	"net/http"
	"strings"
)

const currentAPIVersion = "v1"

// versionedAPI keeps the original /api routes working while exposing the
// stable /api/v1 contract to new clients.
func versionedAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") {
			r.URL.Path = "/api" + strings.TrimPrefix(path, "/api/v1")
			if r.URL.RawPath != "" {
				r.URL.RawPath = "/api" + strings.TrimPrefix(r.URL.RawPath, "/api/v1")
			}
			w.Header().Set("X-Basis-API-Version", currentAPIVersion)
		}
		next.ServeHTTP(w, r)
	})
}
