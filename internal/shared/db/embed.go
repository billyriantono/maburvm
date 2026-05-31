// Package db provides the embedded SQL migrations so they ship inside the
// binary and apply regardless of the process working directory.
package db

import "embed"

// MigrationsFS holds every *.sql migration under the migrations/ directory.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
