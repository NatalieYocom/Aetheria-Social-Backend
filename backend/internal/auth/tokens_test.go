package auth_test

import (
	"testing"
	"time"

	"basisvr-social-service/internal/auth"
)

func TestTokenManagerIssuesAndVerifiesTypedTokens(t *testing.T) {
	manager := auth.NewTokenManager("secret", time.Minute, 24*time.Hour)

	pair, err := manager.Issue(auth.TokenSubject{
		UserID:   "user-1",
		ActorID:  "actor-1",
		Username: "alice",
	})
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("expected both access and refresh tokens")
	}

	accessClaims, err := manager.VerifyAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccess returned error: %v", err)
	}
	if accessClaims.Subject.UserID != "user-1" || accessClaims.Subject.ActorID != "actor-1" {
		t.Fatalf("unexpected access subject: %+v", accessClaims.Subject)
	}
	if accessClaims.TokenType != auth.TokenTypeAccess {
		t.Fatalf("TokenType = %q", accessClaims.TokenType)
	}

	refreshClaims, err := manager.VerifyRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatalf("VerifyRefresh returned error: %v", err)
	}
	if refreshClaims.TokenType != auth.TokenTypeRefresh {
		t.Fatalf("TokenType = %q", refreshClaims.TokenType)
	}

	if _, err := manager.VerifyRefresh(pair.AccessToken); err == nil {
		t.Fatal("access token should not verify as a refresh token")
	}
}
