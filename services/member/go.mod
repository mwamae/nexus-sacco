module github.com/nexussacco/member

go 1.26.3

require (
	github.com/go-chi/chi/v5 v5.2.5
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nexussacco/finance v0.0.0
	github.com/shopspring/decimal v1.4.0
)

// finance is the in-tree shared executor module; resolves via the
// workspace in dev (go.work `use ./services/finance`) and via this
// replace inside the Docker build context. Same pattern services/mpesa
// uses for the same dep.
replace github.com/nexussacco/finance => ../finance

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)
