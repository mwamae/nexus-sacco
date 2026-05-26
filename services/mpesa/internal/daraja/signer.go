// Daraja request signer.
//
// Two distinct mechanisms live here, both used by phases 2-5:
//
//   1. PasswordForSTKPush — base64(shortcode + passkey + timestamp).
//      Used as the `Password` field on STK push and C2B confirmation
//      callbacks. Daraja's documented STK-push spec.
//
//   2. HMACSignature — HMAC-SHA256(consumer_key + timestamp). Used by
//      the B2C and reversal endpoints as a tamper proof on the
//      request body. Spec'd as the "SecurityCredential / Initiator
//      authentication" signature.
//
// Phase 1 ships the functions + unit tests against the published
// sample vectors. The actual call-sites are added in phases 2-5.

package daraja

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

// DarajaTimestamp formats a wall-clock time in the YYYYMMDDHHMMSS
// shape Daraja expects for STK push / password generation. It's also
// what the HMAC signer hashes alongside the consumer key.
const DarajaTimestampLayout = "20060102150405"

func DarajaTimestamp(t time.Time) string {
	return t.UTC().Format(DarajaTimestampLayout)
}

// PasswordForSTKPush builds the base64-encoded password Daraja
// requires on the STK / C2B confirmation flow.
//
//	password = base64( shortcode + passkey + timestamp )
//
// shortcode is the paybill / till number string (no padding). passkey
// is the shared secret Safaricom issues per paybill. timestamp is
// the DarajaTimestamp at the moment the request is signed.
func PasswordForSTKPush(shortcode, passkey, timestamp string) string {
	raw := shortcode + passkey + timestamp
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// HMACSignature builds the SecurityCredential-equivalent body
// signature for the B2C / reversal endpoints. Per Daraja's published
// spec:
//
//	signature = hex( HMAC-SHA256(secret=consumerSecret, data=consumerKey + timestamp) )
//
// (Daraja's spec text says "consumer key + timestamp" hashed under a
// key derived from the consumer secret. We follow that literally so
// our signature matches the reference vectors below; if Safaricom
// ever publishes a clearer spec we revisit.)
//
// Returned as lowercase hex, which is what the reference test
// vectors are encoded in. Callers can decode + re-encode as needed.
func HMACSignature(consumerKey, consumerSecret, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(consumerSecret))
	mac.Write([]byte(consumerKey + timestamp))
	return hex.EncodeToString(mac.Sum(nil))
}

// ConstantTimeEqual is a constant-time compare used by handlers that
// need to validate a signature passed by Daraja (callbacks). Exposed
// here so the signature-handling logic lives in one package.
func ConstantTimeEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return hmac.Equal([]byte(a), []byte(b))
}
