// Package migrations exposes the goose SQL migrations as an embed.FS, so
// integration tests apply exactly the files production uses.
package migrations

import "embed"

// FS holds every *.sql migration at its root, as goose expects.
//
//go:embed *.sql
var FS embed.FS
