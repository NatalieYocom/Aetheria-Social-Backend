package page

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	want := Cursor{SortTime: time.Now().UTC().Truncate(time.Microsecond), ID: uuid.New()}
	raw, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ID != want.ID || !got.SortTime.Equal(want.SortTime) {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestParseRequestValidatesLimitAndCursor(t *testing.T) {
	request := httptest.NewRequest("GET", "/items?limit=51", nil)
	if _, err := ParseRequest(request, 24, 50); err == nil {
		t.Fatal("expected invalid limit")
	}
	request = httptest.NewRequest("GET", "/items?cursor=not-base64", nil)
	if _, err := ParseRequest(request, 24, 50); err == nil {
		t.Fatal("expected invalid cursor")
	}
}
