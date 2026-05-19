// Package auth — password hashing (argon2id) and JWT issuance/verification.
//
// Argon2id parameters target ~50ms on a modern server core. Tune in
// production by measuring on your hardware. The encoded format is the
// PHC string standard so we can rotate parameters without DB migrations.

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultArgon2 = Argon2Params{
	Memory:      64 * 1024, // 64 MiB
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

var (
	ErrInvalidHash         = errors.New("auth: invalid password hash format")
	ErrIncompatibleVersion = errors.New("auth: incompatible argon2 version")
)

func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, DefaultArgon2)
}

func HashPasswordWith(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	b64 := base64.RawStdEncoding.EncodeToString
	encoded := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		b64(salt), b64(key),
	)
	return encoded, nil
}

// VerifyPassword returns nil if the password matches the encoded hash.
// Constant-time comparison; safe to use with attacker-controlled input.
func VerifyPassword(password, encoded string) error {
	p, salt, key, err := decodeArgon2(encoded)
	if err != nil {
		return err
	}
	candidate := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	if subtle.ConstantTimeCompare(key, candidate) == 1 {
		return nil
	}
	return errors.New("auth: password mismatch")
}

func decodeArgon2(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return Argon2Params{}, nil, nil, ErrIncompatibleVersion
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	p.SaltLength = uint32(len(salt))
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
