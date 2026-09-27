package migrations

import "embed"

//go:embed *.sql

// FS carries the ordered migration set — goose applies *.sql by name.
var FS embed.FS
