module github.com/nexussacco/savings

go 1.26.3

require (
	github.com/go-chi/chi/v5 v5.2.5
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nexussacco/shared v0.0.0
	github.com/shopspring/decimal v1.4.0
)

// shared is the umbrella in-tree module holding cross-service Go
// code (healthx, future telemetry/validation helpers). Resolves via
// the workspace in dev (go.work `use ./shared`) and via this
// replace inside the Docker build context.
replace github.com/nexussacco/shared => ../../shared

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jung-kurt/gofpdf v1.16.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)
