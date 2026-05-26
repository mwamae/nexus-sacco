// Standalone TLS config builder so client.go isn't dragged into a
// crypto/tls import when the pin list is empty (the common case in
// phase 1). Phase 6 populates the pin list and this file becomes the
// canonical place to apply tightened cipher suites etc.

package daraja

import (
	"crypto/tls"
	"crypto/x509"
)

func newTLSConfig(pool *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}
}
