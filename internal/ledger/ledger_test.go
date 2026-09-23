package ledger

import (
	"database/sql"
	"errors"
	"fmt"
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

// TestUpsertProposalRevivesAWithdrawnRowWithIdenticalEvidence is the
// regression test for a bug code review found empirically, before this
// shipped: once withdrawn, a proposal whose evidence is static content
// (the B7c memory-finding kinds - no daily-changing number in it) could
// never return to pending even if the exact same true finding reappeared,
// because UpsertProposal's "evidence unchanged, leave alone" fast path did
// not distinguish withdrawn from dismissed. Unlike a dismissal, withdrawal
// is the generator's own opinion, not a human's decision, so it must not be
// sticky the same way.
func TestUpsertProposalRevivesAWithdrawnRowWithIdenticalEvidence(t *testing.T) {
	db := openTestDB(t)
	row := ProposalRow{Kind: "broken_link", Subject: "store/file.md", Evidence: "{}", EvidenceHash: "h1", SampleSize: 1}

	if ok, err := db.UpsertProposal(row, time.Now()); err != nil || !ok {
		t.Fatalf("initial insert = (%v, %v), want (true, nil)", ok, err)
	}
	pending, _ := db.ListProposals(true)
	id := pending[0].ID

	if err := db.WithdrawStalePending(map[ProposalIdentity]bool{}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProposal(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalWithdrawn {
		t.Fatalf("setup: Status = %q, want withdrawn", got.Status)
	}

	// The exact same finding, byte-identical evidence and hash, comes back.
	if ok, err := db.UpsertProposal(row, time.Now()); err != nil || !ok {
		t.Fatalf("revival upsert = (%v, %v), want (true, nil) - a withdrawn row must not be a no-op", ok, err)
	}
	got, err = db.GetProposal(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalPending {
		t.Errorf("Status after revival = %q, want pending - a withdrawal is the generator's opinion, "+
			"not a human's, so it must not be sticky the way a dismissal is", got.Status)
	}

	// The other direction still holds: a dismissal with unchanged evidence
	// stays dismissed, not revived by the same mechanism.
	other := ProposalRow{Kind: "broken_link", Subject: "store/other.md", Evidence: "{}", EvidenceHash: "h2", SampleSize: 1}
	if _, err := db.UpsertProposal(other, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, _ = db.ListProposals(true)
	var otherID int64
	for _, p := range pending {
		if p.Subject == "store/other.md" {
			otherID = p.ID
		}
	}
	if err := db.DismissProposal(otherID); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.UpsertProposal(other, time.Now()); err != nil || ok {
		t.Errorf("re-upserting a dismissed proposal with unchanged evidence = (%v, %v), want (false, nil)", ok, err)
	}
	got, err = db.GetProposal(otherID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalDismissed {
		t.Errorf("Status = %q, want dismissed to survive unchanged evidence", got.Status)
	}
}

// TestUpsertProposalRevivalRespectsThePendingCap confirms a revived
// proposal is not a free pass around MaxPendingProposals: reviving a
// withdrawn row starts occupying a pending slot it was not counted against
// a moment ago, so it has to clear the same cap a brand new proposal would.
func TestUpsertProposalRevivalRespectsThePendingCap(t *testing.T) {
	db := openTestDB(t)
	withdrawnRow := ProposalRow{Kind: "broken_link", Subject: "store/withdrawn.md", Evidence: "{}", EvidenceHash: "h1", SampleSize: 1}
	if _, err := db.UpsertProposal(withdrawnRow, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.WithdrawStalePending(map[ProposalIdentity]bool{}); err != nil {
		t.Fatal(err)
	}

	for i := range MaxPendingProposals {
		row := ProposalRow{Kind: "broken_link", Subject: fmt.Sprintf("store/fill-%d.md", i), Evidence: "{}", EvidenceHash: "h", SampleSize: 1}
		if ok, err := db.UpsertProposal(row, time.Now()); err != nil || !ok {
			t.Fatalf("filling the cap: upsert %d = (%v, %v), want (true, nil)", i, ok, err)
		}
	}
	if n, _ := db.CountPendingProposals(); n != MaxPendingProposals {
		t.Fatalf("setup: %d pending, want the cap of %d full", n, MaxPendingProposals)
	}

	if ok, err := db.UpsertProposal(withdrawnRow, time.Now()); err != nil || ok {
		t.Errorf("reviving into a full queue = (%v, %v), want (false, nil) - revival must respect the cap", ok, err)
	}
}

// TestNeedsIngestBackfillsOnFeatureVersionBump is the regression test for
// issue #49: a file whose size hasn't changed since before a new derived
// table shipped never got that table's data, forever, because size alone
// can't tell "unchanged content" from "unchanged content but loom has grown
// a new table since". InsertRun always stamps the current feature version,
// so simulating "ingested by an older loom" means writing feature_version
// back down by hand, the same way a real pre-bump ledger would read.
func TestNeedsIngestBackfillsOnFeatureVersionBump(t *testing.T) {
	db := openTestDB(t)
	const path = "old-session.jsonl"

	if err := db.InsertRun(RunRecord{Path: path, SizeBytes: 1000, Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	if needs, err := db.NeedsIngest(path, 1000); err != nil || needs {
		t.Fatalf("setup: NeedsIngest right after InsertRun = (%v, %v), want (false, nil)", needs, err)
	}

	if _, err := db.sql.Exec(`UPDATE runs SET feature_version = 0 WHERE path = ?`, path); err != nil {
		t.Fatal(err)
	}

	// Same size as before, but stamped below CurrentFeatureVersion - must
	// re-ingest even though nothing about the file itself changed.
	if needs, err := db.NeedsIngest(path, 1000); err != nil || !needs {
		t.Errorf("NeedsIngest on an unchanged-size run below CurrentFeatureVersion = (%v, %v), want (true, nil) - this is the backfill gap", needs, err)
	}

	// Re-ingesting (InsertRun always stamps CurrentFeatureVersion) settles it.
	if err := db.InsertRun(RunRecord{Path: path, SizeBytes: 1000, Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	if needs, err := db.NeedsIngest(path, 1000); err != nil || needs {
		t.Errorf("NeedsIngest after re-ingesting = (%v, %v), want (false, nil)", needs, err)
	}
}

// TestMigrateFeatureVersionBackfillsExistingRuns confirms a ledger that
// predates the feature_version column (every ledger before this change)
// gets every one of its runs re-ingested once, automatically: DEFAULT 0 on
// ADD COLUMN backfills existing rows with 0, which is below
// CurrentFeatureVersion, so NeedsIngest reports true for all of them without
// anyone needing to know the column was ever missing.
func TestMigrateFeatureVersionBackfillsExistingRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE,
			size_bytes INTEGER NOT NULL DEFAULT 0, kind TEXT NOT NULL,
			weighted_cost REAL NOT NULL DEFAULT 0
		);
		INSERT INTO runs (path, size_bytes, kind) VALUES ('pre-existing.jsonl', 500, 'session');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path) // runs the migration
	if err != nil {
		t.Fatalf("Open on a pre-feature-version ledger failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if needs, err := db.NeedsIngest("pre-existing.jsonl", 500); err != nil || !needs {
		t.Errorf("NeedsIngest on a migrated pre-existing run, same size = (%v, %v), want (true, nil) - it must backfill once", needs, err)
	}
}
