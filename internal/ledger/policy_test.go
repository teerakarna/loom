package ledger

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestPolicyUpsertGetDelete(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()

	if p, err := db.GetPolicy("Explore"); err != nil || p != nil {
		t.Fatalf("GetPolicy on an empty table = (%v, %v), want (nil, nil)", p, err)
	}

	row := PolicyRow{CriteriaVersion: "v1", AgentType: "Explore", Model: "haiku", Effort: "low", Source: "human"}
	if err := db.UpsertPolicy(row, now); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPolicy("Explore")
	if err != nil || got == nil {
		t.Fatalf("GetPolicy = (%v, %v), want a row", got, err)
	}
	if got.Model != "haiku" {
		t.Errorf("Model = %q, want haiku", got.Model)
	}

	// One row per agent type: a second write replaces rather than accumulates.
	row.Model = "sonnet"
	if err := db.UpsertPolicy(row, now); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetPolicy("Explore")
	if got.Model != "sonnet" {
		t.Errorf("Model = %q, want the replaced sonnet", got.Model)
	}

	if err := db.DeletePolicy("Explore"); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.GetPolicy("Explore"); p != nil {
		t.Error("expected the policy to be gone after delete, returning resolution to the default")
	}
}

func TestStatsByAgentTypeUsesMedianAndExcludesUnattributed(t *testing.T) {
	db := openTestDB(t)

	// One wild outlier plus four ordinary runs. A mean would be dragged to
	// ~20200; the median should stay near the typical run.
	costs := []float64{100, 100, 100, 100, 100000}
	for i, c := range costs {
		if err := db.InsertRun(RunRecord{
			Path: fmt.Sprintf("a-%d.jsonl", i), Kind: "agent", AgentType: "Explore",
			Model: "claude-haiku-4-5", WeightedCost: c, ToolUseCount: 3,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An agent run with no attributable type must not become its own bucket.
	if err := db.InsertRun(RunRecord{Path: "orphan.jsonl", Kind: "agent", WeightedCost: 999}); err != nil {
		t.Fatal(err)
	}
	// Session runs are not agent types either.
	if err := db.InsertRun(RunRecord{Path: "s.jsonl", Kind: "session", WeightedCost: 5000}); err != nil {
		t.Fatal(err)
	}

	stats, err := db.StatsByAgentType()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d agent types, want 1 (unattributed runs are not a type): %+v", len(stats), stats)
	}
	s := stats[0]
	if s.AgentType != "Explore" || s.Runs != 5 {
		t.Errorf("got %+v, want Explore with 5 runs", s)
	}
	if s.MedianCost != 100 {
		t.Errorf("MedianCost = %v, want 100 (the mean would be ~20180 and describe only the outlier)", s.MedianCost)
	}
	if s.ObservedModel != "claude-haiku-4-5" {
		t.Errorf("ObservedModel = %q", s.ObservedModel)
	}
	// Counterpoint to TestStatsByAgentTypeObservesEffortMode: none of these
	// runs set Effort, so ObservedEffort must stay empty rather than
	// reporting a mode over nothing.
	if s.ObservedEffort != "" {
		t.Errorf("ObservedEffort = %q, want empty - no run here recorded an effort", s.ObservedEffort)
	}
}

// TestStatsByAgentTypeObservesEffortMode is the regression test for issue
// #106: runs.effort was never selected or grouped on, so AgentTypeStats had
// no way to say what effort these runs actually used, even though the raw
// signal was on disk the whole time (see policy.Resolve for why this is
// surfaced in rationale text only, never written into a Decision's Effort
// field the way ObservedModel is into Model).
func TestStatsByAgentTypeObservesEffortMode(t *testing.T) {
	db := openTestDB(t)
	efforts := []string{"high", "high", "medium", ""} // "" (unset) must not win or count
	for i, e := range efforts {
		if err := db.InsertRun(RunRecord{
			Path: fmt.Sprintf("a-%d.jsonl", i), Kind: "agent", AgentType: "Plan", Effort: e,
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := db.StatsByAgentType()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ObservedEffort != "high" {
		t.Fatalf("got %+v, want ObservedEffort = high (2 of 4 runs, the unset one excluded from the count)", stats)
	}
}

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{5}, 5},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
	}
	for _, c := range cases {
		if got := median(c.in); got != c.want {
			t.Errorf("median(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// The caller's slice must not be reordered.
	in := []float64{3, 1, 2}
	_ = median(in)
	if in[0] != 3 {
		t.Errorf("median sorted the caller's slice in place: %v", in)
	}
}

// TestMigratePoliciesUniqueFromOldSchema covers the upgrade path that unit
// tests originally missed: a ledger created before agent_type was UNIQUE kept
// a table without the constraint, and every UpsertPolicy against it failed
// with "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE
// constraint". Found by running against a real ledger.
func TestMigratePoliciesUniqueFromOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Build a ledger with the pre-UNIQUE policies table, including a duplicate
	// agent_type that the rebuild has to collapse.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE policies (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			criteria_version TEXT NOT NULL,
			agent_type       TEXT NOT NULL,
			model            TEXT NOT NULL,
			effort           TEXT,
			created_at       TEXT NOT NULL
		);
		INSERT INTO policies (criteria_version, agent_type, model, effort, created_at)
			VALUES ('v0', 'Explore', 'sonnet', 'medium', '2026-01-01T00:00:00Z');
		INSERT INTO policies (criteria_version, agent_type, model, effort, created_at)
			VALUES ('v0', 'Explore', 'haiku', 'low', '2026-01-02T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path) // runs the migration
	if err != nil {
		t.Fatalf("Open on a pre-UNIQUE ledger failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	// The duplicate collapsed to the most recent row.
	got, err := db.GetPolicy("Explore")
	if err != nil || got == nil {
		t.Fatalf("GetPolicy = (%v, %v), want the surviving row", got, err)
	}
	if got.Model != "haiku" {
		t.Errorf("Model = %q, want haiku (the later of the two duplicates)", got.Model)
	}

	// And the operation that used to fail now works.
	if err := db.UpsertPolicy(PolicyRow{
		CriteriaVersion: "v1", AgentType: "Explore", Model: "opus", Effort: "high", Source: "human",
	}, time.Now()); err != nil {
		t.Fatalf("UpsertPolicy after migration failed: %v", err)
	}
	got, _ = db.GetPolicy("Explore")
	if got.Model != "opus" {
		t.Errorf("Model = %q, want opus after upsert", got.Model)
	}
}

// TestMigrateProposalsUniqueFromOldSchema covers the same upgrade trap that
// bit `policies`: a ledger created before UNIQUE(kind, subject) keeps a table
// without it, and every ON CONFLICT fails. Handled proactively this time
// rather than after the fact.
func TestMigrateProposalsUniqueFromOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE proposals (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			kind         TEXT NOT NULL,
			evidence     TEXT NOT NULL,
			sample_size  INTEGER NOT NULL,
			effect_size  REAL,
			status       TEXT NOT NULL DEFAULT 'pending',
			created_at   TEXT NOT NULL
		);
		INSERT INTO proposals (kind, evidence, sample_size, status, created_at)
			VALUES ('retire_asset', '{}', 0, 'pending', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path) // runs the migration
	if err != nil {
		t.Fatalf("Open on a pre-UNIQUE proposals table failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	// The operation that would have failed now works.
	ok, err := db.UpsertProposal(ProposalRow{
		Kind: "retire_asset", Subject: "/s/x.md", Evidence: "{}", EvidenceHash: "h1",
	}, time.Now())
	if err != nil || !ok {
		t.Fatalf("UpsertProposal after migration = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestStoredTimestampsAreComparableAcrossZones is the regression test for a
// silent ordering bug: timestamps were stored with the local offset, but SQLite
// compares them as strings, so a run at 12:04Z sorted before a policy written
// at 18:04+07:00 even though it happened an hour later. Every unit test passed
// because they all built times in UTC; only real use mixed the two.
func TestStoredTimestampsAreComparableAcrossZones(t *testing.T) {
	db := openTestDB(t)
	tokyo := time.FixedZone("UTC+9", 9*60*60)

	// A policy written on a non-UTC clock.
	applied := time.Date(2026, 9, 13, 20, 0, 0, 0, tokyo) // 11:00 UTC
	if err := db.UpsertPolicy(ledger_policyRow(), applied); err != nil {
		t.Fatal(err)
	}

	// A run that genuinely happened afterwards, recorded in UTC.
	after := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) // 12:00 UTC, one hour later
	if err := db.InsertRun(RunRecord{
		Path: "/after.jsonl", Kind: "agent", AgentType: "Explore",
		Model: "m", WeightedCost: 1, StartedAt: after, EndedAt: after,
	}); err != nil {
		t.Fatal(err)
	}

	pol, err := db.GetPolicy("Explore")
	if err != nil || pol == nil {
		t.Fatalf("GetPolicy = (%v, %v)", pol, err)
	}
	markerTime, err := time.Parse(time.RFC3339, pol.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}

	since, err := db.StatsByAgentTypeSince("Explore", markerTime)
	if err != nil {
		t.Fatal(err)
	}
	if since.Runs != 1 {
		t.Errorf("runs since the marker = %d, want 1 - a later run must not sort before an "+
			"earlier one just because the offsets differ", since.Runs)
	}
}

func ledger_policyRow() PolicyRow {
	return PolicyRow{
		CriteriaVersion: "v1", AgentType: "Explore", Model: "m",
		Source: "evidence", SampleSize: 20, BaselineMedianCost: 100,
	}
}
