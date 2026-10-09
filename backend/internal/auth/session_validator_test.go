package auth

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionValidatorFailClosedAndCalledOnceAcrossMiddleware(t *testing.T) {
	for _, mode := range []string{"active", "revoked", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			db, mock := newMiddlewareMockDB(t)
			m := NewTokenManager("test", time.Minute, time.Hour)
			pair, err := m.Issue(TokenSubject{UserID: uuid.NewString(), ActorID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			calls := 0
			validator := func(context.Context, Principal) (bool, error) {
				calls++
				if mode == "unavailable" {
					return false, errors.New("secret backend URL")
				}
				return mode == "active", nil
			}
			h := OptionalMiddleware(m, db, validator)(Middleware(m, db, validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
			r := httptest.NewRequest("GET", "/api/me", nil)
			r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 204
			if mode == "revoked" {
				want = 401
			}
			if mode == "unavailable" {
				want = 503
			}
			if w.Code != want {
				t.Fatalf("status=%d expected%d", w.Code, want)
			}
			if calls != 1 {
				t.Fatalf("validator calls=%d", calls)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
