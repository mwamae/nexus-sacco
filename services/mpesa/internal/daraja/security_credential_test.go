package daraja

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
)

// Round-trip property: an RSA keypair we generate locally encrypts +
// decrypts identically through our encoder. Pins the OAEP/SHA-256
// padding scheme without depending on Safaricom's actual cert.
func TestSecurityCredentialEncoder_RoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	enc, err := NewSecurityCredentialEncoder(pubPEM)
	if err != nil {
		t.Fatalf("NewSecurityCredentialEncoder: %v", err)
	}
	const password = "S@ndbx-init1ator-P@ss"
	got, err := enc.Encode(password)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got == "" {
		t.Fatal("encoded value is empty")
	}

	// Decrypt with the private key + verify we get back the input.
	cipherBytes, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, cipherBytes, nil)
	if err != nil {
		t.Fatalf("rsa decrypt: %v", err)
	}
	if string(plain) != password {
		t.Errorf("round-trip mismatch: got %q want %q", plain, password)
	}
}

func TestSecurityCredentialEncoder_EmptyCertReturnsSentinel(t *testing.T) {
	_, err := NewSecurityCredentialEncoder(nil)
	if !errors.Is(err, ErrNoInitiatorCert) {
		t.Errorf("want ErrNoInitiatorCert, got %v", err)
	}
}

func TestSecurityCredentialEncoder_BadPEM(t *testing.T) {
	_, err := NewSecurityCredentialEncoder([]byte("not a pem block"))
	if err == nil {
		t.Fatal("expected error on bad input")
	}
	if !strings.Contains(err.Error(), "PEM") {
		t.Errorf("error should mention PEM, got: %v", err)
	}
}

func TestSecurityCredentialEncoder_X509CertParse(t *testing.T) {
	// Generate a self-signed cert + verify the encoder picks the
	// RSA public key out of it. Exercises the .cer parsing branch.
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	enc, err := NewSecurityCredentialEncoder(certPEM)
	if err != nil {
		t.Fatalf("X.509 cert parse: %v", err)
	}
	if _, err := enc.Encode("init"); err != nil {
		t.Errorf("encode after X.509 cert load: %v", err)
	}
}
