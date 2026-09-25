package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

func openTestLedger(t *testing.T) *ledger.DB {
	t.Helper()
	db, err := ledger.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertRun(t *testing.T, db *ledger.DB, path string) {
	t.Helper()
	if err := db.InsertRun(ledger.RunRecord{Path: path, Kind: "agent"}); err != nil {
		t.Fatal(err)
	}
}

func knownPaths(t *testing.T, db *ledger.DB) map[string]bool {
	t.Helper()
	rows, err := db.KnownRuns()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Path] = true
	}
	return out
}

// The narrowing is the whole argument of this prune, and without a test it
// reads like an accident waiting to be simplified away. `!ingest.IsTranscript`
// would be the more general predicate and is deliberately not used: its
// complement is unbounded, so a ledger carrying rows from a host that named
// subagent transcripts differently would have every one of them deleted along
// with its child rows, and Walk would skip the same files so nothing would
// re-ingest them. A phantom row is a wrong number; a wrongly deleted row is
// lost data.
func TestPruneWorkflowJournalsOnlyTakesJournals(t *testing.T) {
	db := openTestLedger(t)
	const journal = "/p/sess/subagents/workflows/wf-1/journal.jsonl"
	keep := []string{
		"/p/sess.jsonl",                                   // session transcript
		"/p/sess/subagents/agent-1.jsonl",                 // subagent transcript
		"/p/sess/subagents/workflows/wf-1/agent-2.jsonl",  // workflow's own subagent
		"/p/sess/subagents/workflows/wf-1/whatever.jsonl", // not a transcript, not a journal
	}
	insertRun(t, db, journal)
	for _, p := range keep {
		insertRun(t, db, p)
	}

	if err := pruneWorkflowJournals(db); err != nil {
		t.Fatal(err)
	}

	got := knownPaths(t, db)
	if got[journal] {
		t.Errorf("journal %s survived the prune", journal)
	}
	for _, p := range keep {
		if !got[p] {
			t.Errorf("prune deleted %s, which it must never touch", p)
		}
	}
}

// Every one of these states looked like "gone from disk" in the first version
// of this classifier, and the out-of-root one is not rare: the root is a
// command argument, and the ledger deliberately holds runs from outside it.
// Against a narrowed root that misreported 221 real transcripts as prunable,
// and pointed them at a `loom report` that would never have touched them.
//
// The `unexpected` case is the same lesson one step further on: a row under the
// root that Walk will never return and the prune will never delete belongs in
// neither of the two buckets that name a remedy. Without this assertion the
// classifier can go back to ending in a bare `outsideRoot++`, which is a false
// statement on both halves for a row that never clears.
func TestFreshnessSeparatesTheNotWalkedStates(t *testing.T) {
	db := openTestLedger(t)
	root := t.TempDir()
	outside := t.TempDir()

	write := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	current := write("proj/sess.jsonl")
	journal := write("proj/sess/subagents/workflows/wf-1/journal.jsonl")
	// Under the root, on disk, and not a path Walk returns: not prunable, not
	// missing, not outside the root. Nothing explains it, which is the point.
	odd := write("proj/sess/subagents/workflows/wf-1/other.jsonl")
	elsewhere := filepath.Join(outside, "other.jsonl")
	if err := os.WriteFile(elsewhere, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(root, "proj", "deleted.jsonl")

	for _, p := range []string{current, journal, odd, elsewhere, gone} {
		insertRun(t, db, p)
	}
	// Sizes must match for `current` to count as current rather than stale.
	fi, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: current, Kind: "session", SizeBytes: fi.Size()}); err != nil {
		t.Fatal(err)
	}

	f, err := freshness(db, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		got  int
	}{
		{"current", f.current},
		{"prunable", f.prunable},
		{"missing", f.missing},
		{"outsideRoot", f.outsideRoot},
		{"unexpected", f.unexpected},
	} {
		if c.got != 1 {
			t.Errorf("%s = %d, want 1 (counts: %+v)", c.name, c.got, f)
		}
	}
	if f.unreadable != 0 {
		t.Errorf("unreadable = %d, want 0 (counts: %+v)", f.unreadable, f)
	}
	// The freshness totals are read against each other, so they have to add up.
	if f.current+f.stale+f.unseen+f.unreadableOnDisk != f.onDisk {
		t.Errorf("current+stale+unseen+unreadable != onDisk (counts: %+v)", f)
	}
}

// A projects root that does not exist is a new install, and the ledger rows are
// still worth classifying. Any other walk failure leaves a partial file list,
// where continuing would report live transcripts as unexpected - and the version
// this replaces went further and returned every count as zero, printing
// "0 transcripts on disk, 0 ingested" against a ledger holding hundreds of rows.
// Absence read as confirmed-empty is the failure this project has already had
// once, in the memory store.
func TestFreshnessOnAMissingRootStillClassifiesLedgerRows(t *testing.T) {
	db := openTestLedger(t)
	root := filepath.Join(t.TempDir(), "never-created")
	insertRun(t, db, filepath.Join(root, "proj", "sess.jsonl"))

	f, err := freshness(db, root)
	if err != nil {
		t.Fatalf("freshness on a missing root: %v", err)
	}
	if f.onDisk != 0 {
		t.Errorf("onDisk = %d, want 0 (counts: %+v)", f.onDisk, f)
	}
	if f.missing != 1 {
		t.Errorf("missing = %d, want 1 - the row was dropped rather than classified (counts: %+v)", f.missing, f)
	}
}
