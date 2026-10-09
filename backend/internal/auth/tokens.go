package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

type TokenSubject struct {
	UserID   string `json:"userId"`
	ActorID  string `json:"actorId"`
	Username string `json:"username"`
	Version  int64  `json:"version"`
}

type TokenClaims struct {
	Subject   TokenSubject `json:"subject"`
	TokenType string       `json:"tokenType"`
	SessionID string       `json:"sid"`
	jwt.RegisteredClaims
}

type TokenPair struct {
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken"`
	TokenType             string `json:"tokenType"`
	AccessTokenExpiresIn  int64  `json:"accessTokenExpiresIn"`
	RefreshTokenExpiresIn int64  `json:"refreshTokenExpiresIn"`
}

type TokenManager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewTokenManager(secret string, accessTTL, refreshTTL time.Duration) TokenManager {
	return TokenManager{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

func (m TokenManager) Issue(subject TokenSubject) (TokenPair, error) {
	return m.IssueSession(subject, uuid.NewString(), time.Now().UTC().Add(m.refreshTTL))
}

func (m TokenManager) IssueSession(subject TokenSubject, sessionID string, absoluteExpiry time.Time) (TokenPair, error) {
	now := time.Now().UTC()
	refreshTTL := min(m.refreshTTL, absoluteExpiry.Sub(now))
	accessTTL := min(m.accessTTL, refreshTTL)
	if accessTTL < time.Second {
		return TokenPair{}, errors.New("session expired")
	}
	access, err := m.sign(subject, sessionID, TokenTypeAccess, accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := m.sign(subject, sessionID, TokenTypeRefresh, refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", AccessTokenExpiresIn: int64(accessTTL.Seconds()), RefreshTokenExpiresIn: int64(refreshTTL.Seconds())}, nil
}

func (m TokenManager) VerifyAccess(rawToken string) (*TokenClaims, error) {
	return m.verifyTyped(rawToken, TokenTypeAccess)
}

func (m TokenManager) VerifyRefresh(rawToken string) (*TokenClaims, error) {
	return m.verifyTyped(rawToken, TokenTypeRefresh)
}

func (m TokenManager) sign(subject TokenSubject, sessionID, tokenType string, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := TokenClaims{
		Subject:   subject,
		TokenType: tokenType,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: subject.UserID,
			Issuer:  "basis-social", Audience: jwt.ClaimStrings{"basis-social-api"}, ID: uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m TokenManager) verifyTyped(rawToken string, expectedType string) (*TokenClaims, error) {
	claims := &TokenClaims{}
	token, err := jwt.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	}, jwt.WithExpirationRequired(), jwt.WithIssuer("basis-social"), jwt.WithAudience("basis-social-api"), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.TokenType != expectedType {
		return nil, errors.New("unexpected token type")
	}
	if _, err := uuid.Parse(claims.SessionID); err != nil {
		return nil, errors.New("session required; sign in again")
	}
	if _, err := uuid.Parse(claims.ID); err != nil {
		return nil, errors.New("token id required")
	}
	if claims.RegisteredClaims.Subject != claims.Subject.UserID {
		return nil, errors.New("invalid subject")
	}
	return claims, nil
}
