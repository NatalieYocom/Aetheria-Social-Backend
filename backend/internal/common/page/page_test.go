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

func TestTextCursorRoundTrip(t *testing.T) {
	want := TextCursor{SortText: "alice@example.social", ID: uuid.New()}
	raw, err := EncodeText(want)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}
	got, err := DecodeText(raw)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got.ID != want.ID || got.SortText != want.SortText {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestCursorKindsCannotBeMixed(t *testing.T) {
	timeCursor, err := Encode(Cursor{SortTime: time.Now().UTC(), ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeText(timeCursor); err == nil {
		t.Fatal("expected time cursor to be rejected as text cursor")
	}

	textCursor, err := EncodeText(TextCursor{SortText: "alice", ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(textCursor); err == nil {
		t.Fatal("expected text cursor to be rejected as time cursor")
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
