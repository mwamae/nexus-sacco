package crypto

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestEnvelope_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	sealer, err := NewSealer("kms-test-001", key)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range [][]byte{
		[]byte("consumer_secret_value_42"),
		bytes.Repeat([]byte{0x00}, 1),
		bytes.Repeat([]byte{0xff}, 4096),
		{}, // empty plaintext is legal; envelope still has nonce + tag
	} {
		ct, err := sealer.Encrypt(tc)
		if err != nil {
			t.Fatalf("encrypt %d bytes: %v", len(tc), err)
		}
		got, err := sealer.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt %d bytes: %v", len(tc), err)
		}
		if !bytes.Equal(got, tc) {
			t.Errorf("round-trip mismatch: want %x, got %x", tc, got)
		}
		// Header inspection should expose the key id without
		// decrypting.
		id, err := KeyIDOf(ct)
		if err != nil {
			t.Fatalf("keyid: %v", err)
		}
		if id != "kms-test-001" {
			t.Errorf("KeyIDOf = %q, want kms-test-001", id)
		}
	}
}

func TestEnvelope_TamperRejection(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sealer, _ := NewSealer("k1", key)
	ct, err := sealer.Encrypt([]byte("the password is hunter2"))
	if err != nil {
		t.Fatal(err)
	}

	// Flip every byte in the ciphertext, one at a time. Decrypt must
	// reject all of them. This catches any future regression where
	// AAD is dropped or the auth tag is not validated.
	for i := range ct {
		mut := append([]byte(nil), ct...)
		mut[i] ^= 0xff
		if _, err := sealer.Decrypt(mut); err == nil {
			t.Errorf("tampering byte %d went undetected", i)
		}
	}
}

func TestEnvelope_WrongKey(t *testing.T) {
	keyA := make([]byte, 32)
	keyB := make([]byte, 32)
	_, _ = rand.Read(keyA)
	_, _ = rand.Read(keyB)

	sealerA, _ := NewSealer("kA", keyA)
	sealerB, _ := NewSealer("kB", keyB)

	ct, err := sealerA.Encrypt([]byte("only kA can read this"))
	if err != nil {
		t.Fatal(err)
	}
	// Sealer B doesn't know about kA at all — should report
	// ErrUnknownKeyID, not just ErrAuthFailed.
	if _, err := sealerB.Decrypt(ct); !errors.Is(err, ErrUnknownKeyID) {
		t.Errorf("decrypt with wrong sealer: want ErrUnknownKeyID, got %v", err)
	}

	// If we register kA's id with kB's key bytes, the auth tag will
	// fail — distinguishable failure mode (ErrAuthFailed) so audit
	// logs can tell "key missing" from "key wrong".
	sealerB.Keys["kA"] = keyB
	if _, err := sealerB.Decrypt(ct); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("decrypt with mismatched key: want ErrAuthFailed, got %v", err)
	}
}

func TestEnvelope_MalformedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sealer, _ := NewSealer("k1", key)

	for name, ct := range map[string][]byte{
		"empty":               {},
		"missing magic":       []byte("XXXX\x02k1nonce..............."),
		"truncated":           []byte("NXSE\x02k1nonce"), // shorter than header+nonce
		"key id len overruns": []byte("NXSE\xffabc"),     // claims 255-byte key id but only 3 follow
	} {
		if _, err := sealer.Decrypt(ct); err == nil {
			t.Errorf("%s: decrypt should have errored", name)
		}
	}
}
