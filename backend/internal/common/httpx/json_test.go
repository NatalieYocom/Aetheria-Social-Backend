package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerErrorsNeverExposeInternalMessage(t *testing.T) {
	for _, status := range []int{500, 502, 503} {
		w := httptest.NewRecorder()
		WriteError(w, status, "database_failed", "SQL connection password=private-secret")
		if strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), "SQL") {
			t.Fatal("internal error leaked")
		}
		if !strings.Contains(w.Body.String(), "database_failed") {
			t.Fatal("stable code lost")
		}
	}
}
