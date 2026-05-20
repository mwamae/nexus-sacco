// JWT (HS256) access-token parsing. Savings service only verifies tokens
// minted by identity; it never issues them. Claims shape must stay in
// lockstep with services/identity/internal/auth/token.go.

package auth

import (
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
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
	Secret []byte
	Issuer string
}

func NewIssuer(secret []byte, issuer string) *TokenIssuer {
	return &TokenIssuer{Secret: secret, Issuer: issuer}
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

func (c *AccessClaims) HasPermission(p string) bool {
	if c.IsPlatformAdmin {
		return true
	}
	for _, perm := range c.Permissions {
		if perm == p {
			return true
		}
	}
	return false
}
