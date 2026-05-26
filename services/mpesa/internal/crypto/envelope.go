// Envelope encryption for Daraja credentials.
//
// Algorithm: AES-256-GCM. Each ciphertext carries a small header so we
// can rotate keys without re-encrypting in place:
//
//   ┌─────────┬───────────────┬──────────────────┬────────────┬────────────┐
//   │ magic   │ key_id_len    │ key_id bytes     │ nonce      │ ciphertext │
//   │ 4 bytes │ 1 byte (u8)   │ key_id_len bytes │ 12 bytes   │ N bytes    │
//   └─────────┴───────────────┴──────────────────┴────────────┴────────────┘
//
// The magic prefix lets future versions of this codec detect a
// legacy/wrong format and refuse rather than producing garbage. The
// key_id lets the Sealer choose the right master key when rotation is
// in progress (Phase 6); for now we only carry a single key but the
// header is forward-compatible.
//
// Properties tested:
//   - round-trip: Decrypt(Encrypt(x)) == x
//   - tamper detection: any single-byte flip in the ciphertext (or
//     the nonce, or the header) is rejected by GCM's auth tag
//   - wrong-key rejection: a Sealer built with a different key
//     refuses the ciphertext outright (not just returns gibberish)

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrBadCiphertext = errors.New("envelope: malformed or truncated ciphertext")
	ErrUnknownKeyID  = errors.New("envelope: ciphertext was sealed with an unknown key id")
	ErrAuthFailed    = errors.New("envelope: ciphertext failed authentication")
)

// magic is "NXSE" — Nexus Sacco Envelope. 4 bytes is enough to
// distinguish from raw bytea / hex / base64 without taking visible
// space.
var magic = [4]byte{'N', 'X', 'S', 'E'}

const nonceSize = 12 // 96-bit GCM nonce, standard

// Sealer holds one or more master keys and seals/opens ciphertexts
// with the active one. Multiple keys exist in transit during a
// rotation: new writes use ActiveID; reads consult Keys for the id
// stamped in the header.
type Sealer struct {
	ActiveID string
	Keys     map[string][]byte
}

// NewSealer constructs a Sealer with a single active key. The most
// common case in dev/test. Use NewSealerWithKeys when introducing a
// second key for rotation.
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
	return &Sealer{
		ActiveID: activeID,
		Keys:     map[string][]byte{activeID: key},
	}, nil
}

// Encrypt wraps plaintext in the envelope using the active key.
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
	// GCM Seal appends the tag to the ciphertext; AAD = header bytes
	// so a tampered header is rejected by the auth check.
	header := out[:len(magic)+1+len(keyIDBytes)]
	out = gcm.Seal(out, nonce, plaintext, header)
	return out, nil
}

// Decrypt opens an envelope. Returns ErrBadCiphertext / ErrUnknownKeyID
// / ErrAuthFailed for the distinguishable failure modes; callers can
// `errors.Is(...)` to surface a useful message to the operator.
func (s *Sealer) Decrypt(ciphertext []byte) ([]byte, error) {
	// Header: magic(4) + key_id_len(1) + key_id + nonce(12)
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

// KeyIDOf peeks at the header without decrypting. Useful for audit
// logging (e.g. "stored under kms-dev-001") or for a future rotation
// job that needs to discover which rows still use the old key.
func KeyIDOf(ciphertext []byte) (string, error) {
	if len(ciphertext) < len(magic)+1 || !bytesEqual(ciphertext[:4], magic[:]) {
		return "", ErrBadCiphertext
	}
	keyIDLen := int(ciphertext[4])
	if len(ciphertext) < 5+keyIDLen {
		return "", ErrBadCiphertext
	}
	return string(ciphertext[5 : 5+keyIDLen]), nil
}

// internal: bytes.Equal without dragging the bytes package import
// just for one call.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	// Constant-time on the magic prefix is overkill but cheap.
	return v == 0
}

// VarintLength is a parking spot for a future move to variable-length
// key ids if 255 turns out to be too small. Not used today.
var _ = binary.MaxVarintLen64
