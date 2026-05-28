// Umbrella module for shared cross-service Go code.
//
// One module, many packages. healthx is the first; future shared
// helpers (telemetry, common envelopes, validation primitives) land
// alongside without spinning up per-package modules.
//
// Each service that imports a shared package adds:
//
//   require github.com/nexussacco/shared v0.0.0
//   replace github.com/nexussacco/shared => ../../shared
//
// (Same pattern services/mpesa + services/member use for the
// in-tree finance module.) The workspace `use ./shared` in
// /go.work picks the local copy up for dev builds; the replace
// keeps Docker builds working when each service's build context is
// scoped to its own directory.

module github.com/nexussacco/shared

go 1.26.3

require github.com/jackc/pgx/v5 v5.9.2

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.30.0 // indirect
)
