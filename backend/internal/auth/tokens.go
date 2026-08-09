package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
	access, err := m.sign(subject, TokenTypeAccess, m.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := m.sign(subject, TokenTypeRefresh, m.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:           access,
		RefreshToken:          refresh,
		TokenType:             "Bearer",
		AccessTokenExpiresIn:  int64(m.accessTTL.Seconds()),
		RefreshTokenExpiresIn: int64(m.refreshTTL.Seconds()),
	}, nil
}

func (m TokenManager) VerifyAccess(rawToken string) (*TokenClaims, error) {
	return m.verifyTyped(rawToken, TokenTypeAccess)
}

func (m TokenManager) VerifyRefresh(rawToken string) (*TokenClaims, error) {
	return m.verifyTyped(rawToken, TokenTypeRefresh)
}

func (m TokenManager) sign(subject TokenSubject, tokenType string, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := TokenClaims{
		Subject:   subject,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject.UserID,
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
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.TokenType != expectedType {
		return nil, errors.New("unexpected token type")
	}
	return claims, nil
}
