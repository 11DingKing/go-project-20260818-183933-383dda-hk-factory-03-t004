// Package store persists sitesync entities in SQLite. It owns the connection,
// migrations, transaction helpers and repository implementations. The HTTP and
// service layers depend only on the repository interfaces declared here.
package store

import _ "embed"

//go:embed schema.sql
var schemaSQL string
