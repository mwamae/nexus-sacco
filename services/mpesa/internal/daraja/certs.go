// Safaricom Daraja root-certificate pinning.
//
// Phase 1 shipped with the pin loader scaffolded but no pins. Phase 6
// wires the load path: production.pem is go:embed'd at build time +
// passed to PinnedRootCertsPEM when MPESA_FORCE_SANDBOX=false. The
// placeholder file (-----BEGIN PLACEHOLDER-----) is detected at
// startup and rejects the boot in production env.
//
// For sandbox traffic the system CA store is fine — Safaricom's
// sandbox host serves a publicly-trusted cert. Production deployments
// MUST replace certs/production.pem with the Safaricom-issued chain;
// see the comment in that file for portal steps.

package daraja

import (
	_ "embed"
	"strings"
)

//go:embed certs/production.pem
var productionCertBytes []byte

// PinnedRootCertsPEM is populated at startup by LoadProductionPins
// when MPESA_FORCE_SANDBOX=false. Empty in sandbox to keep the
// system roots in effect (the sandbox host's chain is publicly trusted).
//
// The Client constructor in client.go reads this slice. If you ever
// need an extra cert (a corporate proxy MITM, for example) append it
// here AND extend the production.pem placeholder so the cert content
// has one source of truth.
var PinnedRootCertsPEM = [][]byte{}

// LoadProductionPins inspects the embedded production.pem and, when
// it contains real PEM blocks (not the placeholder header), copies
// them into PinnedRootCertsPEM. Returns true when at least one cert
// was loaded.
//
// Called from the Client constructor when ForceSandbox is false.
// In production env (config.Env == "production") the caller checks
// the return value + refuses to start when false.
func LoadProductionPins() (loaded bool) {
	if len(productionCertBytes) == 0 {
		return false
	}
	// The placeholder file ships with a "-----BEGIN PLACEHOLDER-----"
	// header (instead of "-----BEGIN CERTIFICATE-----"). When that's
	// still present, treat the file as unconfigured.
	if strings.Contains(string(productionCertBytes), "BEGIN PLACEHOLDER") {
		return false
	}
	if !strings.Contains(string(productionCertBytes), "BEGIN CERTIFICATE") {
		return false
	}
	PinnedRootCertsPEM = append(PinnedRootCertsPEM[:0], append([]byte(nil), productionCertBytes...))
	return true
}
