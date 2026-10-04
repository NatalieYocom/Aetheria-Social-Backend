package profiles

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/page"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestSearchUsersEnrichesOnlyDisplayedPageWithOneRelationshipQuery(t *testing.T) {
	db, mock := newProfileMockDB(t)
	viewer, first, extra := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectQuery("FROM users u").WithArgs("%a%", nil, uuid.Nil, 2).WillReturnRows(profileRows().AddRow(uuid.New(), first, "alice", "alice@example.test", "Alice", "", "", "", "", []byte(`[]`)).AddRow(uuid.New(), extra, "amy", "amy@example.test", "Amy", "", "", "", "", []byte(`[]`)))
	mock.ExpectQuery("FROM relationships WHERE").WithArgs(viewer, dbx.PostgresTextArray([]string{first.String()})).WillReturnRows(sqlmock.NewRows([]string{"actor", "type", "direction", "state", "viewer_row"}).AddRow(first, "friend", "mutual", "accepted", true))
	req := httptest.NewRequest("GET", "/api/search/users?q=a&limit=1", nil)
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), auth.Principal{ActorID: viewer}))
	res := httptest.NewRecorder()
	NewHandler(db).SearchUsers(res, req)
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("personalized search response cacheable")
	}
	if res.Code != 200 {
		t.Fatalf("status%d: %s", res.Code, res.Body.String())
	}
	var result page.Response[publicProfileResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Pagination.NextCursor == nil || !result.Data[0].Relationship.Friend || result.Data[0].Relationship.FriendState != "accepted" {
		t.Fatalf("wrong relationship/page: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestRelationshipBatchRespectsPendingDirectionSelfAndBilateralBlock(t *testing.T) {
	db, mock := newProfileMockDB(t)
	viewer, outgoing, incoming, blocked := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	profiles := []publicProfileResponse{{ActorID: viewer}, {ActorID: outgoing}, {ActorID: incoming}, {ActorID: blocked}}
	mock.ExpectQuery("FROM relationships WHERE").WithArgs(viewer, dbx.PostgresTextArray([]string{outgoing.String(), incoming.String(), blocked.String()})).WillReturnRows(sqlmock.NewRows([]string{"actor", "type", "direction", "state", "viewer_row"}).
		AddRow(outgoing, "friend", "outgoing", "pending", true).
		AddRow(incoming, "friend", "outgoing", "pending", false).
		AddRow(blocked, "friend", "mutual", "accepted", true).
		AddRow(blocked, "block", "outgoing", "accepted", false))
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{ActorID: viewer})
	if err := NewHandler(db).enrichRelationships(ctx, profiles); err != nil {
		t.Fatal(err)
	}
	if !profiles[0].Relationship.Self {
		t.Fatal("self missing")
	}
	if profiles[1].Relationship.FriendState != "pending" || profiles[1].Relationship.FriendDirection != "outgoing" {
		t.Fatal("outgoing state incorrect")
	}
	if profiles[2].Relationship.FriendState != "pending" || profiles[2].Relationship.FriendDirection != "incoming" {
		t.Fatal("incoming state incorrect")
	}
	if !profiles[3].Relationship.Blocked || profiles[3].Relationship.Friend || profiles[3].Relationship.FriendState != "none" {
		t.Fatal("bilateral block must dominate stale friendship")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestAnonymousRelationshipsDoNotQueryOrReuseViewerState(t *testing.T) {
	db, mock := newProfileMockDB(t)
	profiles := []publicProfileResponse{{ActorID: uuid.New(), Relationship: relationshipView{Friend: true, Blocked: true, FriendState: "accepted"}}}
	if err := NewHandler(db).enrichRelationships(context.Background(), profiles); err != nil {
		t.Fatal(err)
	}
	if profiles[0].Relationship.Friend || profiles[0].Relationship.Blocked || profiles[0].Relationship.FriendState != "none" {
		t.Fatal("viewer state leaked to anonymous request")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
