// Package schema holds the SQLite DDL for the todo database.
//
// The DDL is kept as plain SQL rather than as Go string literals so it can be
// piped straight into a sqlite3 shell, diffed, and read by hand — which the
// design sketch assumes it will be.
package schema

import _ "embed"

// SQL is the complete schema: pragmas, tables, and indexes.
//
//go:embed schema.sql
var SQL string
