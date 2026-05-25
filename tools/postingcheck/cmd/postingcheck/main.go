// postingcheck: command-line entry point.
//
// Usage:
//   go run ./cmd/postingcheck ../../services/savings/internal/handler/...
//
// Plugged into `make lint` at the repo root.

package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/nexussacco/tools/postingcheck"
)

func main() {
	singlechecker.Main(postingcheck.Analyzer)
}
