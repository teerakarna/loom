// Package ledger is the SQLite-backed (WAL mode) local store: artifacts,
// events, runs, policies, proposals, and coordination. No content or
// full-text index - derived metrics and identifiers only (design doc,
// "Privacy by construction"). See docs/design.md, "Ledger".
//
// B1 only writes to runs; the other five tables are created now
// (schema-complete from the start) but populated starting in later phases -
// artifacts and events from B2 onward (see artifact.go and event.go).
package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver - keeps the single-static-binary,
	// cross-compile-from-one-machine property from docs/design.md ("Core
	// engine"); a CGO driver would need a C toolchain per target platform.
)

// DB wraps the underlying SQLite connection.
type DB struct {
	sql *sql.DB
}

// policiesSchema is separate so the rebuild migration can recreate the table
// from the same definition rather than a drifting copy.
const proposalsSchema = `CREATE TABLE IF NOT EXISTS proposals (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	kind          TEXT NOT NULL,
	subject       TEXT NOT NULL DEFAULT '', -- what it is about: an artifact path, an agent type
	evidence      TEXT NOT NULL, -- JSON
	evidence_hash TEXT NOT NULL DEFAULT '',
	sample_size   INTEGER NOT NULL,
	effect_size   REAL,
	status        TEXT NOT NULL DEFAULT 'pending', -- pending | dismissed
	created_at    TEXT NOT NULL,
	UNIQUE(kind, subject)
);`

const policiesSchema = `CREATE TABLE IF NOT EXISTS policies (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	criteria_version TEXT NOT NULL,
	agent_type       TEXT NOT NULL UNIQUE,
	model            TEXT NOT NULL,
	effort           TEXT,
	source           TEXT NOT NULL DEFAULT 'human', -- 'human' | 'evidence'
	sample_size      INTEGER NOT NULL DEFAULT 0,    -- runs behind an 'evidence' row; 0 for 'human'
	-- The marker that closes the loop: what this policy was measured against
	-- when it was applied, so later runs can be compared like for like.
	-- created_at is the marker time. Zero for a policy set by hand, which has
	-- no measured baseline to regress from.
	baseline_median_cost   REAL NOT NULL DEFAULT 0,
	baseline_denial_rate   REAL NOT NULL DEFAULT 0,
	baseline_feedback_rate REAL NOT NULL DEFAULT 0,
	created_at       TEXT NOT NULL
);`

const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	type        TEXT NOT NULL,
	path        TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	first_seen  TEXT NOT NULL,
	last_seen   TEXT NOT NULL
);

-- Append-only. This IS the ledger - never UPDATE or DELETE a row here.
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
	size_bytes          INTEGER NOT NULL DEFAULT 0, -- file size at ingest; a change means re-ingest
	session_id          TEXT,
	kind                TEXT NOT NULL, -- "session" | "agent"
	model               TEXT,
	lane                TEXT NOT NULL DEFAULT '', -- project directory the session ran in; '' if unattributable
	agent_type          TEXT NOT NULL DEFAULT '', -- from the .meta.json companion; '' for sessions
	effort              TEXT NOT NULL DEFAULT '', -- reasoning effort; '' when the transcript carried none
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
	-- found yet). Deliberately NOT reconciled against weighted_cost - see
	-- docs/transcript-schema.md, "Reconciliation does NOT hold".
	reported_subagent_tokens INTEGER,
	reported_tool_uses       INTEGER,
	reported_duration_ms     INTEGER
);

` + policiesSchema + `

` + proposalsSchema + `

-- tool_usage and compactions are B7b: occupancy, not cost. Both are derived
-- metrics and identifiers only (constraint 6) - see docs/design.md, "B7b.
-- Occupancy metrics".
CREATE TABLE IF NOT EXISTS tool_usage (
	run_id       INTEGER NOT NULL REFERENCES runs(id),
	tool_name    TEXT NOT NULL,
	calls        INTEGER NOT NULL DEFAULT 0,
	result_bytes INTEGER NOT NULL DEFAULT 0, -- a byte count, never a token estimate
	PRIMARY KEY (run_id, tool_name)
);

CREATE TABLE IF NOT EXISTS compactions (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id             INTEGER NOT NULL REFERENCES runs(id),
	-- The host's own record id. A resumed session replays its prior
	-- compaction history verbatim, uuid included - this is what dedup keys
	-- on, so the same event is never counted twice across two transcripts
	-- (docs/transcript-schema.md, "compact_boundary").
	boundary_uuid      TEXT NOT NULL UNIQUE,
	seq                INTEGER NOT NULL DEFAULT 0, -- order within run_id's own file
	trigger            TEXT NOT NULL DEFAULT '',
	pre_tokens         INTEGER NOT NULL DEFAULT 0,
	post_tokens        INTEGER NOT NULL DEFAULT 0,
	-- As reported by the host: cumulative WITHIN one session lineage, not
	-- safely summable across events. See ToolOutputReport / CompactionSummary
	-- for the per-event figure ingest actually sums.
	cumulative_dropped INTEGER NOT NULL DEFAULT 0,
	duration_ms        INTEGER NOT NULL DEFAULT 0,
	at                 TEXT -- nullable, same as runs.started_at/ended_at: a
	                        -- transcript line with no parseable timestamp is
	                        -- not grounds for failing the whole insert
);

-- artifact_usage is B7a (#39): the join that lets "unused" be a question
-- the ledger can answer. Two structured, ground-truth signals only - a
-- Skill tool_use's skill name, resolved to the artifact it names, and a
-- Read/Edit/Write tool_use's file_path, matched by exact equality - both
-- resolved against the artifacts table at write time (see usage.go).
-- Never a message-text mention: a skill's name and description appear in
-- every session's system prompt whether invoked or not, and an earlier
-- attempt at string-matching gave every artifact a near-identical count.
CREATE TABLE IF NOT EXISTS artifact_usage (
	run_id        INTEGER NOT NULL REFERENCES runs(id),
	artifact_path TEXT NOT NULL REFERENCES artifacts(path),
	uses          INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (run_id, artifact_path)
);

CREATE TABLE IF NOT EXISTS coordination (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	kind    TEXT NOT NULL, -- "message" | "presence" | "task"
	payload TEXT NOT NULL, -- JSON
	ts      TEXT NOT NULL
);
`

// Open opens (creating if necessary) the SQLite database at path, in WAL
// mode, and applies the schema. Safe to call repeatedly - every statement is
// idempotent (CREATE TABLE IF NOT EXISTS).
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &DB{sql: db}, nil
}

// migrate adds columns that shipped after a table's original CREATE TABLE,
// for databases created by an earlier version of Loom. CREATE TABLE IF NOT
// EXISTS (above) only ever applies to a table that doesn't exist yet, so a
// column added later needs its own ALTER TABLE here - guarded by checking
// the table's actual columns first, since SQLite has no ADD COLUMN IF NOT
// EXISTS. artifacts.name/description were added after B1 shipped the table
// schema-complete but column-incomplete (docs/design.md, "B1 only writes to
// runs and events" - the table existed before the selector needed these).
func migrate(db *sql.DB) error {
	for _, m := range []struct {
		table   string
		columns map[string]string
	}{
		{"artifacts", map[string]string{
			"name":        `ALTER TABLE artifacts ADD COLUMN name TEXT NOT NULL DEFAULT ''`,
			"description": `ALTER TABLE artifacts ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		}},
		{"runs", map[string]string{
			"agent_type": `ALTER TABLE runs ADD COLUMN agent_type TEXT NOT NULL DEFAULT ''`,
			"effort":     `ALTER TABLE runs ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
		}},
		{"runs", map[string]string{
			"size_bytes": `ALTER TABLE runs ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0`,
			"lane":       `ALTER TABLE runs ADD COLUMN lane TEXT NOT NULL DEFAULT ''`,
		}},
		{"proposals", map[string]string{
			"subject":       `ALTER TABLE proposals ADD COLUMN subject TEXT NOT NULL DEFAULT ''`,
			"evidence_hash": `ALTER TABLE proposals ADD COLUMN evidence_hash TEXT NOT NULL DEFAULT ''`,
		}},
		{"policies", map[string]string{
			"source":                 `ALTER TABLE policies ADD COLUMN source TEXT NOT NULL DEFAULT 'human'`,
			"sample_size":            `ALTER TABLE policies ADD COLUMN sample_size INTEGER NOT NULL DEFAULT 0`,
			"baseline_median_cost":   `ALTER TABLE policies ADD COLUMN baseline_median_cost REAL NOT NULL DEFAULT 0`,
			"baseline_denial_rate":   `ALTER TABLE policies ADD COLUMN baseline_denial_rate REAL NOT NULL DEFAULT 0`,
			"baseline_feedback_rate": `ALTER TABLE policies ADD COLUMN baseline_feedback_rate REAL NOT NULL DEFAULT 0`,
		}},
	} {
		if err := addMissingColumns(db, m.table, m.columns); err != nil {
			return err
		}
	}
	return migratePoliciesUnique(db)
}

// migratePoliciesUnique rebuilds the policies table when it predates the
// UNIQUE constraint on agent_type.
//
// ALTER TABLE cannot add a constraint in SQLite, and CREATE TABLE IF NOT
// EXISTS does nothing to a table that already exists, so a ledger created
// before the constraint kept a policies table without it - and every
// UpsertPolicy against that table failed with "ON CONFLICT clause does not
// match any PRIMARY KEY or UNIQUE constraint". Found by running against a real
// ledger, not by unit tests, which all built their table fresh.
//
// The constraint is load-bearing rather than cosmetic: one row per agent type
// is what bounds this table's growth (constraint 10).
func migratePoliciesUnique(db *sql.DB) error {
	if err := addUniqueByRebuild(db, "policies", policiesSchema,
		"criteria_version, agent_type, model, effort, source, sample_size, "+
			"baseline_median_cost, baseline_denial_rate, baseline_feedback_rate, created_at",
		"agent_type"); err != nil {
		return err
	}
	return addUniqueByRebuild(db, "proposals", proposalsSchema,
		"kind, subject, evidence, evidence_hash, sample_size, effect_size, status, created_at",
		"kind, subject")
}

// addUniqueByRebuild adds a UNIQUE constraint to a table that predates it.
// SQLite's ALTER TABLE cannot add a constraint, and CREATE TABLE IF NOT EXISTS
// does nothing to a table that already exists, so a ledger created before the
// constraint keeps a table without it - and every ON CONFLICT against that
// table fails with "ON CONFLICT clause does not match any PRIMARY KEY or
// UNIQUE constraint".
//
// Generalised after hitting this once on `policies`, where every unit test
// passed because each built its table fresh and only a real upgraded ledger
// exposed it. The second table to need it should not need the bug again.
func addUniqueByRebuild(db *sql.DB, table, schema, columns, groupBy string) error {
	var sqlText string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no table yet; the CREATE above builds it correctly
	}
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "UNIQUE") {
		return nil
	}

	// Keep the most recent row per group if the old table accumulated
	// duplicates that the constraint would now reject.
	old := table + "_old"
	stmts := []string{
		`ALTER TABLE ` + table + ` RENAME TO ` + old,
		schema,
		`INSERT INTO ` + table + ` (` + columns + `) SELECT ` + columns +
			` FROM ` + old + ` WHERE id IN (SELECT MAX(id) FROM ` + old + ` GROUP BY ` + groupBy + `)`,
		`DROP TABLE ` + old,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("rebuilding %s table: %w", table, err)
		}
	}
	return nil
}

// addMissingColumns adds any of columns (name -> ALTER statement) that table
// does not already have. SQLite has no ADD COLUMN IF NOT EXISTS, so the
// table's actual columns are read first.
func addMissingColumns(db *sql.DB, table string, columns map[string]string) error {
	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	for col, ddl := range columns {
		if have[col] {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.sql.Close()
}

// NeedsIngest reports whether the transcript at path should be read: either it
// has never been ingested, or its size has changed since it was.
//
// Size, not just presence. An earlier version keyed on path alone, justified by
// a comment asserting that an edited-in-place transcript "shouldn't happen -
// Claude Code only appends". That premise was backwards: appending is exactly
// what happens, continuously, for the whole life of a session. Any session
// ingested while still running was frozen at that moment permanently, and no
// amount of re-running `loom report` would correct it, because the path was
// already known. On a real corpus the largest run was understated by roughly
// half. Silent under-counting, in the one number the tool exists to get right.
//
// A byte-offset checkpoint (docs/design.md, "Ingest") is the efficient version
// and belongs with the fsnotify-following work. Re-reading a changed file is
// the obviously correct version, and ingest is fast enough that correctness is
// the better trade today.
func (d *DB) NeedsIngest(path string, size int64) (bool, error) {
	var stored int64
	err := d.sql.QueryRow(`SELECT size_bytes FROM runs WHERE path = ?`, path).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return stored != size, nil
}
