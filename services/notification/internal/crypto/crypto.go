// AES-GCM symmetric encryption for SMTP passwords and (later)
// Africa's Talking API keys. The key is the first 32 bytes of the
// supplied secret (typically JWT_SECRET — same secret already lives
// in every service's env so no new key management).
//
// Ciphertext format on disk:  base64(nonce || ciphertext || tag)

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Encrypt returns a base64-encoded AES-GCM ciphertext.
func Encrypt(key []byte, plaintext string) (string, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, []byte(plaintext), nil)
	out := append(nonce, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt.
func Decrypt(key []byte, b64 string) (string, error) {
	if b64 == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode b64: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) < ns+aead.Overhead() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("crypto: key must be >= 32 bytes (got %d)", len(key))
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
