module github.com/nexussacco/mpesa

go 1.26.3

require (
	github.com/go-chi/chi/v5 v5.2.5
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nexussacco/finance v0.0.0
	github.com/shopspring/decimal v1.4.0
)

// finance is an in-tree module; resolution comes from the repo-root
// go.work file. Listed here so non-workspace tooling that reads
// go.mod alone (e.g. some IDE indexers) still picks up the
// dependency relationship. Version stays at v0.0.0 because the
// module is never published to a registry.
replace github.com/nexussacco/finance => ../finance

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)
