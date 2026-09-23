package ledger

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db1.Close()

	db2, err := Open(path) // re-opening an existing db must not fail
	if err != nil {
		t.Fatal(err)
	}
	_ = db2.Close()
}

func TestInsertAndReportRun(t *testing.T) {
	db := openTestDB(t)

	subTok := int64(777)
	toolUses := int64(3)
	dur := int64(5000)

	if err := db.InsertRun(RunRecord{
		Path: "session-a.jsonl", SessionID: "s1", Kind: "session", Model: "claude-sonnet-5",
		StartedAt: time.Now(), EndedAt: time.Now(),
		InputTokens: 300, OutputTokens: 80, WeightedCost: 500,
		ToolUseCount: 2, DenialCount: 1, FeedbackCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{
		Path: "agent-a.jsonl", SessionID: "s1", Kind: "agent", Model: "claude-haiku-4-5",
		WeightedCost: 250, ToolUseCount: 3,
		ReportedSubagentTokens: &subTok, ReportedToolUses: &toolUses, ReportedDurationMs: &dur,
	}); err != nil {
		t.Fatal(err)
	}

	needs, err := db.NeedsIngest("session-a.jsonl", 0)
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("NeedsIngest for an unchanged file = true, want false")
	}
	needs, err = db.NeedsIngest("nonexistent.jsonl", 123)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("NeedsIngest for an unknown file = false, want true")
	}

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalRuns != 2 {
		t.Errorf("TotalRuns = %d, want 2", s.TotalRuns)
	}
	if s.SessionRuns != 1 || s.AgentRuns != 1 {
		t.Errorf("SessionRuns=%d AgentRuns=%d, want 1,1", s.SessionRuns, s.AgentRuns)
	}
	if s.TotalWeightedCost != 750 {
		t.Errorf("TotalWeightedCost = %v, want 750", s.TotalWeightedCost)
	}
	if s.TotalToolUses != 5 {
		t.Errorf("TotalToolUses = %d, want 5", s.TotalToolUses)
	}
	if s.UnreconciledAgents != 1 {
		t.Errorf("UnreconciledAgents = %d, want 1 (one agent run has a reported figure)", s.UnreconciledAgents)
	}
	if len(s.ByModel) != 2 {
		t.Errorf("ByModel = %+v, want 2 entries", s.ByModel)
	}
}

// TestInsertRunReplacesRatherThanDuplicating covers a deliberate change: this
// used to assert that a second insert for the same path errored. It now
// replaces, because a live transcript grows and must be re-read (see
// NeedsIngest). "One file, one run" is preserved by replacement, not by
// refusal.
func TestInsertRunReplacesRatherThanDuplicating(t *testing.T) {
	db := openTestDB(t)
	rec := RunRecord{Path: "dup.jsonl", Kind: "session", SizeBytes: 10, WeightedCost: 1}
	if err := db.InsertRun(rec); err != nil {
		t.Fatal(err)
	}
	rec.SizeBytes, rec.WeightedCost = 20, 2
	if err := db.InsertRun(rec); err != nil {
		t.Fatalf("re-inserting a grown file must succeed, got %v", err)
	}

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalRuns != 1 {
		t.Errorf("TotalRuns = %d, want 1", s.TotalRuns)
	}
	if s.TotalWeightedCost != 2 {
		t.Errorf("TotalWeightedCost = %v, want 2 (the replacement, not the sum)", s.TotalWeightedCost)
	}
}

// TestNeedsIngestDetectsAGrownFile is the regression test for silent cost
// under-counting. Ingest previously keyed on path alone, so a session
// transcript that grew after being ingested was frozen at its first reading
// forever. On a real corpus the largest run was understated by roughly half.
func TestNeedsIngestDetectsAGrownFile(t *testing.T) {
	db := openTestDB(t)
	const path = "live-session.jsonl"

	// Never seen: must ingest.
	if needs, err := db.NeedsIngest(path, 1000); err != nil || !needs {
		t.Fatalf("NeedsIngest on an unknown path = (%v, %v), want (true, nil)", needs, err)
	}

	if err := db.InsertRun(RunRecord{Path: path, SizeBytes: 1000, Kind: "session", WeightedCost: 500}); err != nil {
		t.Fatal(err)
	}

	// Same size: already current, do not re-read.
	if needs, err := db.NeedsIngest(path, 1000); err != nil || needs {
		t.Errorf("NeedsIngest on an unchanged file = %v, want false", needs)
	}

	// Grown, which is what a live session does continuously.
	if needs, err := db.NeedsIngest(path, 2500); err != nil || !needs {
		t.Errorf("NeedsIngest on a grown file = %v, want true - this is the bug", needs)
	}

	// Re-ingesting replaces the row rather than adding a second one, and the
	// new cost supersedes the stale one.
	if err := db.InsertRun(RunRecord{Path: path, SizeBytes: 2500, Kind: "session", WeightedCost: 1200}); err != nil {
		t.Fatal(err)
	}
	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalRuns != 1 {
		t.Errorf("TotalRuns = %d, want 1 (one file is one run, even re-ingested)", s.TotalRuns)
	}
	if s.TotalWeightedCost != 1200 {
		t.Errorf("TotalWeightedCost = %v, want 1200 (the fresh reading, not the stale one)", s.TotalWeightedCost)
	}
}

// TestMigrateAssetRenameDropsOldTablesAndProposals is a regression test for
// the artifact->asset rename: a pre-rename ledger has `artifacts`/
// `artifact_usage` tables and may carry a pending `proposals` row stored
// under an old kind string. Both must be gone after Open runs the
// migration - the tables because CREATE TABLE IF NOT EXISTS never rebuilds
// a table that already exists under its old name, and the proposal row
// because propose.TouchesUserFiles no longer recognizes its kind and would
// otherwise mis-report it as safe (found by code review before this
// shipped).
func TestMigrateAssetRenameDropsOldTablesAndProposals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, path TEXT UNIQUE,
			name TEXT, description TEXT, status TEXT, first_seen TEXT, last_seen TEXT
		);
		INSERT INTO artifacts (type, path, status, first_seen, last_seen)
			VALUES ('skill', '/s/old.md', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		CREATE TABLE artifact_usage (
			tool_use_id TEXT PRIMARY KEY, run_id INTEGER, artifact_path TEXT
		);
		CREATE TABLE proposals (
			id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
			subject TEXT NOT NULL DEFAULT '', evidence TEXT NOT NULL,
			evidence_hash TEXT NOT NULL DEFAULT '', sample_size INTEGER NOT NULL,
			effect_size REAL, status TEXT NOT NULL DEFAULT 'pending', created_at TEXT NOT NULL,
			UNIQUE(kind, subject)
		);
		INSERT INTO proposals (kind, subject, evidence, sample_size, created_at)
			VALUES ('retire_artifact', '/s/old.md', '{}', 1, '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path) // runs migrateAssetRename
	if err != nil {
		t.Fatalf("Open on a pre-rename ledger failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	var name string
	for _, table := range []string{"artifacts", "artifact_usage"} {
		err := db.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %q still exists after migration (err=%v)", table, err)
		}
	}

	rows, err := db.ListProposals(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d proposal(s) after migration, want 0 - the retire_artifact row must be purged, not carried forward under a kind TouchesUserFiles no longer recognizes", len(rows))
	}
}
