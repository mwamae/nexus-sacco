// SecurityCredential — Daraja's RSA-OAEP-encrypted initiator
// password.
//
// Every B2C / reversal / balance-query request Daraja signs as the
// "initiator" carries a SecurityCredential field. The field is the
// initiator's password, encrypted under Safaricom's public RSA cert,
// then base64-encoded.
//
// The cert is environment-dependent:
//   • Sandbox: a known, publicly-published RSA key. Operators copy
//     the PEM blob into MPESA_INITIATOR_CERT_PEM in dev .env files.
//   • Production: each SACCO downloads its own initiator cert from
//     the Safaricom portal during paybill setup; this is operator-
//     provided via the same env var. Production deployments MUST
//     also flip MPESA_FORCE_SANDBOX=false in config to talk to the
//     live Daraja host.
//
// We deliberately do NOT bundle the sandbox cert. The decision was
// "every environment supplies its own cert" so dev / sandbox /
// production all follow the same shape — no silent fallback to a
// cert that's been rotated upstream without anyone noticing.

package daraja

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
)

// ErrNoInitiatorCert is returned when an encryption call is made
// without an initiator cert configured. Distributor logs + skips
// the row.
var ErrNoInitiatorCert = errors.New("daraja: no initiator cert configured (MPESA_INITIATOR_CERT_PEM)")

// SecurityCredentialEncoder holds the parsed cert + a single method
// to encrypt the initiator password. One per service-instance is
// enough — the cert is process-local.
type SecurityCredentialEncoder struct {
	pubKey *rsa.PublicKey
}

// NewSecurityCredentialEncoder parses a PEM-encoded certificate (or
// raw public key) from the supplied bytes. Returns ErrNoInitiatorCert
// when the input is empty so callers can decide whether to fail at
// startup or just skip B2C rows.
func NewSecurityCredentialEncoder(pemBytes []byte) (*SecurityCredentialEncoder, error) {
	if len(pemBytes) == 0 {
		return nil, ErrNoInitiatorCert
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("daraja: initiator cert is not PEM-encoded")
	}

	// Try parsing as a full X.509 certificate first (Safaricom's
	// portal hands operators a `.cer` containing the cert chain);
	// fall back to parsing as a bare PKIX public key.
	var pub *rsa.PublicKey
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		var ok bool
		pub, ok = cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("daraja: initiator cert is not RSA")
		}
	} else if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		var ok bool
		pub, ok = key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("daraja: initiator key is not RSA")
		}
	} else {
		return nil, fmt.Errorf("daraja: initiator cert/key parse failed (tried X.509 + PKIX): %w", err)
	}
	return &SecurityCredentialEncoder{pubKey: pub}, nil
}

// Encode encrypts the initiator password under the loaded cert
// using RSA-OAEP / SHA-256 + base64-encodes the ciphertext.
//
// NOTE on padding scheme. Safaricom's docs sometimes say PKCS#1v1.5
// and sometimes RSA-OAEP depending on which page you read. The
// canonical production answer (from the Daraja API console) is
// RSA-OAEP / SHA-256, which is what we use. If you ever see
// "InvalidSecurityCredential" from the sandbox after a cert rotate,
// confirm the padding scheme in the portal before assuming this
// code drifted.
func (e *SecurityCredentialEncoder) Encode(password string) (string, error) {
	if e == nil || e.pubKey == nil {
		return "", ErrNoInitiatorCert
	}
	if password == "" {
		return "", errors.New("daraja: initiator password is empty")
	}
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, e.pubKey, []byte(password), nil)
	if err != nil {
		return "", fmt.Errorf("rsa encrypt: %w", err)
	}
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
