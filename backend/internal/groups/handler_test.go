package groups

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"basisvr-social-service/internal/auth"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestAddWorldPublishesGroupAnnounceInSameTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	managerID := uuid.New()
	groupID := uuid.New()
	groupActorID := uuid.New()
	worldID := uuid.New()
	mock.ExpectQuery("SELECT EXISTS").WithArgs(groupID, managerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT visibility FROM worlds").WithArgs(worldID, managerID).
		WillReturnRows(sqlmock.NewRows([]string{"visibility"}).AddRow("public"))
	mock.ExpectQuery("SELECT actor_id, visibility FROM groups").WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "visibility"}).AddRow(groupActorID, "public"))
	mock.ExpectExec("INSERT INTO group_worlds").WithArgs(groupID, worldID, managerID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT actor_uri, followers_url").WithArgs(groupActorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "followers_url"}).AddRow(
			"https://social.example/groups/builders", "https://social.example/groups/builders/followers",
		))
	mock.ExpectQuery("SELECT DISTINCT").WithArgs(groupActorID).
		WillReturnRows(sqlmock.NewRows([]string{"inbox"}).AddRow("https://remote.example/inbox"))
	mock.ExpectExec("INSERT INTO activities").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), groupActorID, worldID, "World", "public", sqlmock.AnyArg(), "outbound").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO outbox_jobs").WithArgs(sqlmock.AnyArg(), "https://remote.example/inbox").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT actor_id FROM group_members").WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}))

	handler := NewHandler(db, "https://social.example", nil)
	router := chi.NewRouter()
	router.Post("/api/groups/{id}/worlds", func(w http.ResponseWriter, r *http.Request) {
		principal := auth.Principal{UserID: uuid.New(), ActorID: managerID, Username: "alice"}
		handler.AddWorld(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
	})
	req := httptest.NewRequest(http.MethodPost, "/api/groups/"+groupID.String()+"/worlds", strings.NewReader(`{"worldId":"`+worldID.String()+`"}`))
	req = req.WithContext(context.Background())
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
