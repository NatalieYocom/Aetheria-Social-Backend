package auth

import (
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestTokensRequireSessionAudienceIssuerExpiryAndUniqueIDs(t *testing.T) {
	m := NewTokenManager("test-secret", time.Minute, time.Hour)
	subject := TokenSubject{UserID: uuid.NewString(), ActorID: uuid.NewString()}
	pair, err := m.Issue(subject)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := m.Issue(subject)
	if pair.AccessToken == again.AccessToken || pair.RefreshToken == again.RefreshToken {
		t.Fatal("token uniqueness missing")
	}
	original, err := m.VerifyAccess(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []string{"sid", "jti", "issuer", "audience", "expiry"} {
		t.Run(tt, func(t *testing.T) {
			c := *original
			switch tt {
			case "sid":
				c.SessionID = ""
			case "jti":
				c.ID = ""
			case "issuer":
				c.Issuer = "attacker"
			case "audience":
				c.Audience = jwt.ClaimStrings{"other-api"}
			case "expiry":
				c.ExpiresAt = nil
			}
			raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.secret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.VerifyAccess(raw); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}
func TestPasswordByteBoundsBeforeHashing(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("a", 73), strings.Repeat("я", 37)} {
		if _, err := HashPassword(password); err == nil {
			t.Fatal("invalid password length accepted")
		}
	}
	hash, err := HashPassword(strings.Repeat("a", 72))
	if err != nil {
		t.Fatal(err)
	}
	if VerifyPassword(hash, strings.Repeat("a", 73)) {
		t.Fatal("oversized password accepted")
	}
}
