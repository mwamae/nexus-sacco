package daraja

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

// Daraja's published sample vector for STK-push password generation
// (https://developer.safaricom.co.ke). With shortcode 174379, passkey
// "bfb279f9aa9bdbcf158e97dd71a467cd2e0c893059b10f78e6b72ada1ed2c919",
// and timestamp 20160216165627 the password decodes to the literal
// concatenation, which is what we hash. This test pins the format
// against the spec so any future refactor (e.g. swapping base64
// encoder, trimming inputs) is caught immediately.
func TestPasswordForSTKPush_KnownVector(t *testing.T) {
	const (
		shortcode = "174379"
		passkey   = "bfb279f9aa9bdbcf158e97dd71a467cd2e0c893059b10f78e6b72ada1ed2c919"
		timestamp = "20160216165627"
	)
	got := PasswordForSTKPush(shortcode, passkey, timestamp)
	// Round-trip: decoding must yield exactly the concatenation.
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := shortcode + passkey + timestamp
	if string(decoded) != want {
		t.Errorf("password round-trip mismatch:\n  want %q\n  got  %q", want, decoded)
	}
	if got == "" {
		t.Error("password must not be empty")
	}
}

// HMACSignature has to match what an independent HMAC-SHA256
// computation produces for the same input — that's the reference
// vector. We compute it inline rather than hard-coding a hex string
// so this test pins the algorithm + input shape (consumerKey +
// timestamp under the consumerSecret) without bolting us to a
// specific value that could rot if Daraja ever rotates the published
// sample.
func TestHMACSignature_MatchesReferenceComputation(t *testing.T) {
	const (
		ck = "AGE3X29sHLNgwasdfQWqEjjkjk"
		cs = "1jhUUjsWxxxxxxxxYY="
		ts = "20260101120000"
	)
	got := HMACSignature(ck, cs, ts)

	mac := hmac.New(sha256.New, []byte(cs))
	mac.Write([]byte(ck + ts))
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("hmac mismatch:\n  want %s\n  got  %s", want, got)
	}
}

func TestDarajaTimestamp_FormatStable(t *testing.T) {
	when := time.Date(2026, 1, 5, 13, 7, 42, 0, time.UTC)
	if got := DarajaTimestamp(when); got != "20260105130742" {
		t.Errorf("timestamp: got %q, want 20260105130742", got)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Error("equal strings should match")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Error("different strings should not match")
	}
	if !ConstantTimeEqual("  abc  ", "abc") {
		t.Error("constant-time compare should trim before comparing")
	}
}
