// JWT (HS256) access tokens and opaque refresh tokens.
//
// Access tokens carry tenant, user, roles, permissions, and the
// is_platform_admin flag. Other services validate them locally — no
// callback to identity per request.
//
// Refresh tokens are 32 random bytes, base64url-encoded, and stored
// hashed (SHA-256) in Postgres. On refresh we look up by hash, verify
// not-revoked / not-expired, rotate to a new token, mark the old one
// revoked with parent_id linking the chain for theft detection.

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type AccessClaims struct {
	TenantID        string   `json:"tid"`
	TenantSlug      string   `json:"tslug"`
	UserID          string   `json:"sub"`
	Email           string   `json:"email"`
	FullName        string   `json:"name"`
	Roles           []string `json:"roles,omitempty"`
	Permissions     []string `json:"perms,omitempty"`
	IsPlatformAdmin bool     `json:"platform,omitempty"`
	jwt.RegisteredClaims
}

type TokenIssuer struct {
	Secret  []byte
	Issuer  string
	TTL     time.Duration
}

func NewIssuer(secret []byte, issuer string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{Secret: secret, Issuer: issuer, TTL: ttl}
}

func (t *TokenIssuer) Issue(c AccessClaims) (string, time.Time, error) {
	now := time.Now()
	expiry := now.Add(t.TTL)
	c.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    t.Issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiry),
		ID:        uuid.NewString(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(t.Secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return signed, expiry, nil
}

func (t *TokenIssuer) Parse(raw string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return t.Secret, nil
	}, jwt.WithIssuer(t.Issuer), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// ─────────── Refresh tokens (opaque, hashed at rest) ───────────

const refreshTokenBytes = 32

func NewRefreshToken() (raw string, hashed []byte, err error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

func HashRefreshToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
