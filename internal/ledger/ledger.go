// Package ledger is the SQLite-backed (WAL mode) local store: artifacts,
// events, runs, policies, proposals, and coordination. No content or
// full-text index — derived metrics and identifiers only (design doc,
// "Privacy by construction"). See docs/design.md, "Ledger".
//
// B1 only writes to runs and events; the other four tables are created now
// (schema-complete from the start) but populated starting in later phases.
package ledger

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver — keeps the single-static-binary,
	// cross-compile-from-one-machine property from docs/design.md ("Core
	// engine"); a CGO driver would need a C toolchain per target platform.
)

// DB wraps the underlying SQLite connection.
type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	type       TEXT NOT NULL,
	path       TEXT NOT NULL UNIQUE,
	status     TEXT NOT NULL DEFAULT 'active',
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL
);

-- Append-only. This IS the ledger — never UPDATE or DELETE a row here.
CREATE TABLE IF NOT EXISTS events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         TEXT NOT NULL,
	session_id TEXT,
	kind       TEXT NOT NULL,
	payload    TEXT NOT NULL -- JSON
);

CREATE TABLE IF NOT EXISTS runs (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	path                TEXT NOT NULL UNIQUE,
	session_id          TEXT,
	kind                TEXT NOT NULL, -- "session" | "agent"
	model               TEXT,
	started_at          TEXT,
	ended_at            TEXT,
	input_tokens        INTEGER NOT NULL DEFAULT 0,
	output_tokens       INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
	weighted_cost       REAL NOT NULL DEFAULT 0,
	tool_use_count      INTEGER NOT NULL DEFAULT 0,
	denial_count        INTEGER NOT NULL DEFAULT 0,
	feedback_count      INTEGER NOT NULL DEFAULT 0,
	-- Reported by a parent transcript's task-notification <usage> block, for
	-- agent runs only. NULL when unknown (session runs, or no notification
	-- found yet). Deliberately NOT reconciled against weighted_cost — see
	-- docs/transcript-schema.md, "Reconciliation does NOT hold".
	reported_subagent_tokens INTEGER,
	reported_tool_uses       INTEGER,
	reported_duration_ms     INTEGER
);

CREATE TABLE IF NOT EXISTS policies (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	criteria_version TEXT NOT NULL,
	agent_type       TEXT NOT NULL,
	model            TEXT NOT NULL,
	effort           TEXT,
	created_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proposals (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	kind         TEXT NOT NULL,
	evidence     TEXT NOT NULL, -- JSON
	sample_size  INTEGER NOT NULL,
	effect_size  REAL,
	status       TEXT NOT NULL DEFAULT 'pending',
	created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS coordination (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	kind    TEXT NOT NULL, -- "message" | "presence" | "task"
	payload TEXT NOT NULL, -- JSON
	ts      TEXT NOT NULL
);
`

// Open opens (creating if necessary) the SQLite database at path, in WAL
// mode, and applies the schema. Safe to call repeatedly — every statement is
// idempotent (CREATE TABLE IF NOT EXISTS).
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &DB{sql: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.sql.Close()
}

// HasRun reports whether a run for this file path has already been ingested,
// so a re-run of `loom report` doesn't double-count. B1 simplification: this
// checks by path only, not by mtime/size, so an edited-in-place transcript
// (which shouldn't happen — Claude Code only appends) won't be re-ingested.
// A byte-offset checkpoint (per docs/design.md, "Ingest") is the real
// mechanism for the later fsnotify-following enhancement; this is enough for
// B1's retroactive-only scope.
func (d *DB) HasRun(path string) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(1) FROM runs WHERE path = ?`, path).Scan(&n)
	return n > 0, err
}
