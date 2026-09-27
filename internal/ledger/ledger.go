// Package ledger is the SQLite-backed (WAL mode) local store: assets,
// events, runs, policies, proposals, and coordination. No content or
// full-text index - derived metrics and identifiers only (design doc,
// "Privacy by construction"). See docs/design.md, "Ledger".
//
// B1 only writes to runs; the other five tables are created now
// (schema-complete from the start) but populated starting in later phases -
// assets and events from B2 onward (see asset.go and event.go).
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
	subject       TEXT NOT NULL DEFAULT '', -- what it is about: an asset path, an agent type
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
CREATE TABLE IF NOT EXISTS assets (
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
	reported_duration_ms     INTEGER,
	-- Issue #49: size alone answers "did this file change", never "does this
	-- row have every derived table a current loom populates". A run stamped
	-- below CurrentFeatureVersion (run.go) is re-ingested by NeedsIngest
	-- regardless of size, so a feature added after a file was first ingested
	-- backfills onto it instead of silently never applying.
	feature_version          INTEGER NOT NULL DEFAULT 0
);

` + policiesSchema + `

` + proposalsSchema + `

-- tool_usage and compactions are B7b: occupancy, not cost. Both are derived
-- metrics and identifiers only (constraint 6) - see docs/design.md, "B7b.
-- Occupancy metrics".
--
-- One row per tool_use block, not pre-aggregated per (run_id, tool_name):
-- a resumed session replays its prior tool_use/tool_result lines verbatim,
-- the same way it replays compact_boundary records (confirmed on a real
-- corpus - 325 of 1203 tool_use ids shared between an original session and
-- its resumed continuation). tool_use_id is the block's own id, globally
-- unique, and is what dedup keys on - identical in spirit to compactions'
-- boundary_uuid below. A pre-aggregated row had no identity to dedupe
-- against and double-counted every replayed call; found by code review, not
-- by the tests or the dogfooding run that shipped alongside it.
CREATE TABLE IF NOT EXISTS tool_usage (
	tool_use_id  TEXT PRIMARY KEY,
	run_id       INTEGER NOT NULL REFERENCES runs(id),
	tool_name    TEXT NOT NULL,
	result_bytes INTEGER NOT NULL DEFAULT 0 -- a byte count, never a token estimate
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

-- asset_usage is B7a (#39): the join that lets "unused" be a question
-- the ledger can answer. Two structured, ground-truth signals only - a
-- Skill tool_use's skill name, resolved to the asset it names, and a
-- Read/Edit/Write tool_use's file_path, matched by exact equality - both
-- resolved against the assets table at write time (see usage.go).
-- Never a message-text mention: a skill's name and description appear in
-- every session's system prompt whether invoked or not, and an earlier
-- attempt at string-matching gave every asset a near-identical count.
--
-- One row per tool_use block, keyed on its own id - same reasoning and
-- same fix as tool_usage above: a resumed session replays these blocks
-- verbatim, and an aggregated (run_id, asset_path) row had nothing to
-- dedupe a replay against.
CREATE TABLE IF NOT EXISTS asset_usage (
	tool_use_id   TEXT PRIMARY KEY,
	run_id        INTEGER NOT NULL REFERENCES runs(id),
	asset_path TEXT NOT NULL REFERENCES assets(path)
);

CREATE TABLE IF NOT EXISTS coordination (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	kind    TEXT NOT NULL, -- "message" | "presence" | "task"
	payload TEXT NOT NULL, -- JSON
	ts      TEXT NOT NULL
);

-- A singleton row (id fixed at 1 by the CHECK), not one row per pass:
-- there is exactly one <home>/.claude/projects root per ledger, so there is
-- exactly one streak to track. unscanned_since is NULL whenever the root
-- was successfully enumerated last pass (whatever happened before does not
-- matter once it works again) and holds the timestamp of the first pass in
-- an unbroken run of scan failures otherwise - see issue #76,
-- propose.needsProtection's expiry.
CREATE TABLE IF NOT EXISTS memory_root_scan (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	unscanned_since TEXT
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
	// migrate may have dropped a table whose shape predates this version
	// (see dropIfMissingColumn) - re-apply schema so CREATE TABLE IF NOT
	// EXISTS rebuilds it in the current shape. Idempotent against every
	// table that didn't need dropping.
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("re-apply schema after migrate: %w", err)
	}
	return &DB{sql: db}, nil
}

// migrate adds columns that shipped after a table's original CREATE TABLE,
// for databases created by an earlier version of Loom. CREATE TABLE IF NOT
// EXISTS (above) only ever applies to a table that doesn't exist yet, so a
// column added later needs its own ALTER TABLE here - guarded by checking
// the table's actual columns first, since SQLite has no ADD COLUMN IF NOT
// EXISTS. assets.name/description were added after B1 shipped the table
// schema-complete but column-incomplete (docs/design.md, "B1 only writes to
// runs and events" - the table existed before the selector needed these).
func migrate(db *sql.DB) error {
	for _, m := range []struct {
		table   string
		columns map[string]string
	}{
		{"assets", map[string]string{
			"name":        `ALTER TABLE assets ADD COLUMN name TEXT NOT NULL DEFAULT ''`,
			"description": `ALTER TABLE assets ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		}},
		{"runs", map[string]string{
			"agent_type": `ALTER TABLE runs ADD COLUMN agent_type TEXT NOT NULL DEFAULT ''`,
			"effort":     `ALTER TABLE runs ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
		}},
		{"runs", map[string]string{
			"size_bytes": `ALTER TABLE runs ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0`,
			"lane":       `ALTER TABLE runs ADD COLUMN lane TEXT NOT NULL DEFAULT ''`,
		}},
		{"runs", map[string]string{
			// DEFAULT 0 on ADD COLUMN backfills every existing row with 0,
			// which is below CurrentFeatureVersion - so a ledger that
			// predates this column gets every one of its runs re-ingested on
			// the next `loom report`, which is exactly the backfill issue
			// #49 needed and had no mechanism for until now.
			"feature_version": `ALTER TABLE runs ADD COLUMN feature_version INTEGER NOT NULL DEFAULT 0`,
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
	if err := migratePoliciesUnique(db); err != nil {
		return err
	}
	if err := migrateOccupancyDedup(db); err != nil {
		return err
	}
	return migrateAssetRename(db)
}

// migrateAssetRename drops the tables and columns that predate the
// "artifact" -> "asset" rename (docs/design.md, "The 'artifact' noun
// rename"): the old `artifacts` and `artifact_usage` tables, if a
// pre-rename ledger still has them. Both are pure derived caches, rebuilt in
// full by the next `loom report`/`loom advise` - a plain drop leaves nothing
// behind to reconcile, and CREATE TABLE IF NOT EXISTS above already builds
// `assets`/`asset_usage` fresh once the old names are gone. Without this,
// the app would still work (it never looks up a table by its old name), but
// the dead `artifacts`/`artifact_usage` tables would sit in the SQLite file
// forever as orphaned cruft.
func migrateAssetRename(db *sql.DB) error {
	for _, table := range []string{"artifacts", "artifact_usage"} {
		if err := dropTableIfExists(db, table); err != nil {
			return err
		}
	}
	// A pending proposal stored under an old kind string (retire_artifact,
	// unreachable_artifact) is not just cosmetic: propose.TouchesUserFiles no
	// longer recognizes it, so it would fall through to its default answer
	// (false, "safe to automate") for a proposal that in fact touches the
	// user's files - the opposite of correct, found by code review before
	// this shipped. Generate never emits the old kind strings again, so these
	// rows are permanently dead: delete rather than reconcile.
	_, err := db.Exec(`DELETE FROM proposals WHERE kind IN ('retire_artifact', 'unreachable_artifact')`)
	return err
}

// dropTableIfExists drops table unconditionally if it exists at all -
// unlike dropIfMissingColumn, which only drops a table whose current shape
// is missing an expected column. Used where the table's old name is itself
// what identifies it as pre-migration, so there is no column to check.
func dropTableIfExists(db *sql.DB, table string) error {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = db.Exec(`DROP TABLE ` + table)
	return err
}

// migrateOccupancyDedup drops tool_usage/asset_usage if either predates
// the per-tool_use_id dedup fix (found by code review, B7b/B7a): both
// tables are pure derived caches, entirely rebuilt from transcripts by the
// next `loom report`/`loom advise`, so a clean drop is correct and simpler
// than a data-preserving rebuild - the old rows are exactly the
// double-counted values this fix exists to stop trusting.
func migrateOccupancyDedup(db *sql.DB) error {
	for _, table := range []string{"tool_usage", "asset_usage"} {
		if err := dropIfMissingColumn(db, table, "tool_use_id"); err != nil {
			return err
		}
	}
	return nil
}

// dropIfMissingColumn drops table entirely if it exists but its stored
// CREATE statement lacks column - the table predates a schema change too
// structural for ALTER TABLE ADD COLUMN, and the caller has already
// established it holds nothing worth preserving.
func dropIfMissingColumn(db *sql.DB, table, column string) error {
	var sqlText string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no table yet; the next CREATE builds it correctly
	}
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, column) {
		return nil
	}
	_, err = db.Exec(`DROP TABLE ` + table)
	return err
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
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

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

// NeedsIngest reports whether the transcript at path should be read: it has
// never been ingested, its size has changed since it was, or the stored run
// predates a feature that needs backfilling onto it (see CurrentFeatureVersion,
// issue #49).
//
// Size, not just presence, for the first two. An earlier version keyed on path
// alone, justified by a comment asserting that an edited-in-place transcript
// "shouldn't happen - Claude Code only appends". That premise was backwards:
// appending is exactly what happens, continuously, for the whole life of a
// session. Any session ingested while still running was frozen at that moment
// permanently, and no amount of re-running `loom report` would correct it,
// because the path was already known. On a real corpus the largest run was
// understated by roughly half. Silent under-counting, in the one number the
// tool exists to get right.
//
// Feature version, for the third. Confirmed on this machine's own real
// ledger: a file whose size never moved after B7b/B7a's tables shipped kept
// its runs row but never gained a single tool_usage/compactions/asset_usage
// row, because nothing about "did this file change" was ever going to catch
// "did loom gain a new table since I last read this file". Re-reading on a
// version bump, not just a size change, is what closes that gap - and closes
// it for every future feature the same way, not just this one.
//
// A byte-offset checkpoint (docs/design.md, "Ingest") is the efficient version
// of the size check and belongs with the fsnotify-following work. Re-reading a
// changed file is the obviously correct version, and ingest is fast enough
// that correctness is the better trade today.
//
// The size-vs-version decision itself lives in ClassifyFreshness, not here,
// so a freshness-reporting caller needing the three-way answer (stale vs
// needs-reread vs current, not just a yes/no) reads the same predicate this
// re-ingest decision does, rather than an independently maintained copy of
// it drifting from this one (issue #96).
func (d *DB) NeedsIngest(path string, size int64) (bool, error) {
	var storedSize, storedVersion int64
	err := d.sql.QueryRow(`SELECT size_bytes, feature_version FROM runs WHERE path = ?`, path).
		Scan(&storedSize, &storedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return ClassifyFreshness(storedSize, size, storedVersion) != FreshnessCurrent, nil
}
