// Envelope encryption for Phase 6 vendor credentials (CRB +
// insurance). Duplicates the algorithm from
// services/mpesa/internal/crypto/envelope.go so savings doesn't
// depend on the mpesa service's internal package.
//
// If you change anything here, mirror it there — the wire format
// MUST stay byte-compatible. Long-term we'll extract this into
// shared/cryptox; for now duplication is the lower-risk move.
//
// Algorithm: AES-256-GCM with a 4-byte magic prefix + 1-byte key_id
// length + key_id bytes + 12-byte nonce + ciphertext+tag.

package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

var (
	ErrBadCiphertext = errors.New("envelope: malformed or truncated ciphertext")
	ErrUnknownKeyID  = errors.New("envelope: ciphertext was sealed with an unknown key id")
	ErrAuthFailed    = errors.New("envelope: ciphertext failed authentication")
)

var magic = [4]byte{'N', 'X', 'S', 'E'}

const nonceSize = 12

type Sealer struct {
	ActiveID string
	Keys     map[string][]byte
}

func NewSealer(activeID string, key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("envelope: AES-256 key must be 32 bytes, got %d", len(key))
	}
	if activeID == "" {
		return nil, errors.New("envelope: active key id must not be empty")
	}
	if len(activeID) > 255 {
		return nil, errors.New("envelope: active key id must be <= 255 bytes")
	}
	return &Sealer{ActiveID: activeID, Keys: map[string][]byte{activeID: key}}, nil
}

func (s *Sealer) Encrypt(plaintext []byte) ([]byte, error) {
	key, ok := s.Keys[s.ActiveID]
	if !ok {
		return nil, fmt.Errorf("envelope: active key %q not in key map", s.ActiveID)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("envelope: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("envelope: nonce: %w", err)
	}
	keyIDBytes := []byte(s.ActiveID)
	out := make([]byte, 0, len(magic)+1+len(keyIDBytes)+nonceSize+len(plaintext)+gcm.Overhead())
	out = append(out, magic[:]...)
	out = append(out, byte(len(keyIDBytes)))
	out = append(out, keyIDBytes...)
	out = append(out, nonce...)
	header := out[:len(magic)+1+len(keyIDBytes)]
	out = gcm.Seal(out, nonce, plaintext, header)
	return out, nil
}

func (s *Sealer) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < len(magic)+1+nonceSize {
		return nil, ErrBadCiphertext
	}
	if !bytesEqual(ciphertext[:4], magic[:]) {
		return nil, ErrBadCiphertext
	}
	keyIDLen := int(ciphertext[4])
	headerEnd := len(magic) + 1 + keyIDLen
	if keyIDLen == 0 || len(ciphertext) < headerEnd+nonceSize {
		return nil, ErrBadCiphertext
	}
	keyID := string(ciphertext[5:headerEnd])
	key, ok := s.Keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKeyID, keyID)
	}
	nonceStart := headerEnd
	nonce := ciphertext[nonceStart : nonceStart+nonceSize]
	payload := ciphertext[nonceStart+nonceSize:]
	if len(payload) == 0 {
		return nil, ErrBadCiphertext
	}
	header := ciphertext[:headerEnd]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("envelope: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, payload, header)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	return pt, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
