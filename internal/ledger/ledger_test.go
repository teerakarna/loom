package ledger

import (
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

	has, err := db.HasRun("session-a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("HasRun(session-a.jsonl) = false, want true")
	}
	has, err = db.HasRun("nonexistent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("HasRun(nonexistent.jsonl) = true, want false")
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

func TestInsertRunDuplicatePathFails(t *testing.T) {
	db := openTestDB(t)
	rec := RunRecord{Path: "dup.jsonl", Kind: "session"}
	if err := db.InsertRun(rec); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(rec); err == nil {
		t.Error("expected an error inserting a duplicate path, got nil")
	}
}
