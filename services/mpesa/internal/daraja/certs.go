// Safaricom Daraja root-certificate pin list.
//
// Phase 1 (this PR) ships with an empty pin list — the http.Transport
// uses the system roots by default, which is enough to talk to the
// sandbox + production hosts via the public PKI. Phase 6 fills this
// slice with the Safaricom-issued CA chain in PEM form so a hostile
// CA can't intercept production traffic even if it sneaks into a
// node's system store.
//
// To wire the pins back in: set TLSClientConfig.RootCAs on the
// Client's http.Transport in client.go to a new x509.CertPool built
// from this slice. Tests that need to bypass the pin (httptest) can
// detect a development environment and skip the pin install.

package daraja

// PinnedRootCertsPEM is intentionally empty for now. Each entry must
// be a self-contained PEM block ("-----BEGIN CERTIFICATE-----\n...").
var PinnedRootCertsPEM = [][]byte{}
