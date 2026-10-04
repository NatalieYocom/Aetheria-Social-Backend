package profiles

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"basisvr-social-service/internal/auth"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestManagedProfileRejectsCanonicalFieldChangesAtomically(t *testing.T) {
	for _, body := range []string{`{"displayName":"Changed","bio":"Must not persist"}`, `{"avatarUrl":"","bio":"Must not persist"}`} {
		t.Run(body, func(t *testing.T) {
			db, mock := newProfileMockDB(t)
			principal := auth.Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "linked"}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id FROM users WHERE id=\\$1 FOR UPDATE").WithArgs(principal.UserID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(principal.UserID))
			mock.ExpectQuery("SELECT EXISTS").WithArgs(principal.UserID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			mock.ExpectRollback()
			request := httptest.NewRequest("PATCH", "/api/me/profile", strings.NewReader(body))
			request = request.WithContext(auth.ContextWithPrincipal(request.Context(), principal))
			response := httptest.NewRecorder()
			NewHandler(db).UpdateMe(response, request)
			if response.Code != 409 || !strings.Contains(response.Body.String(), `"managed_identity"`) {
				t.Fatalf("unexpected profile rejection: %d %s", response.Code, response.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedProfileAllowsBioWithoutChangingCanonicalFields(t *testing.T) {
	db, mock := newProfileMockDB(t)
	principal := auth.Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "linked"}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM users WHERE id=\\$1 FOR UPDATE").WithArgs(principal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(principal.UserID))
	mock.ExpectExec("UPDATE profiles").WithArgs(principal.UserID, nil, "Independent Social bio", nil, nil, nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("FROM users u").WithArgs(principal.Username).
		WillReturnRows(profileRows().AddRow(principal.UserID, principal.ActorID, principal.Username,
			"linked@social.test", "Canonical BeeBa name", "Independent Social bio", "https://beeba.test/avatar.png", "", "", []byte(`[]`)))
	request := httptest.NewRequest("PATCH", "/api/me/profile", strings.NewReader(`{"bio":"Independent Social bio"}`))
	request = request.WithContext(auth.ContextWithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	NewHandler(db).UpdateMe(response, request)
	var profile publicProfileResponse
	if response.Code != 200 {
		t.Fatalf("profile status=%d", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Bio != "Independent Social bio" || profile.DisplayName != "Canonical BeeBa name" || profile.AvatarURL != "https://beeba.test/avatar.png" {
		t.Fatal("local bio change altered canonical profile fields")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
