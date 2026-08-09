// Package store will hold the database layer: connection handling, identifier
// allocation from the sequence table, the entity types, and event emission.
//
// It is empty for now. The blank import pins the pure-Go SQLite driver so the
// dependency is settled before any of that lands.
package store

import _ "modernc.org/sqlite"
