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
//
// The relative row is the second prunable class and is the migration for this
// branch's own change: absolutising the root fixed the paths going in and did
// nothing for the ones already stored, which can no longer match a walked file
// under any root. Left in place, the next report inserts a second row for the
// same file and the ledger double-counts its cost forever.
func TestPruneUnmatchableRowsTakesOnlyJournalsAndRelativePaths(t *testing.T) {
	db := openTestLedger(t)
	prune := []string{
		"/p/sess/subagents/workflows/wf-1/journal.jsonl", // orchestration journal, no usage
		"p/sess.jsonl",                                   // relative: written by an older `loom report <relative root>`
		"./p/sess/subagents/agent-9.jsonl",               // relative and a real transcript; still unmatchable
	}
	keep := []string{
		"/p/sess.jsonl",                                   // session transcript
		"/p/sess/subagents/agent-1.jsonl",                 // subagent transcript
		"/p/sess/subagents/workflows/wf-1/agent-2.jsonl",  // workflow's own subagent
		"/p/sess/subagents/workflows/wf-1/whatever.jsonl", // not a transcript, not a journal
	}
	for _, p := range append(append([]string{}, prune...), keep...) {
		insertRun(t, db, p)
	}

	if err := pruneUnmatchableRows(db); err != nil {
		t.Fatal(err)
	}

	got := knownPaths(t, db)
	for _, p := range prune {
		if got[p] {
			t.Errorf("unmatchable row %s survived the prune", p)
		}
	}
	for _, p := range keep {
		if !got[p] {
			t.Errorf("prune deleted %s, which it must never touch", p)
		}
	}
}

// A relative row must classify as prunable, and it must do so without asking the
// filesystem anything: the cwd a `loom status` happens to run from is not the one
// the row was written against, so a stat would answer a different question each
// time. Run from a directory where the row's path resolves to a real file under
// the root, which is the case that would otherwise look current or unexplained
// depending on where the user stood.
func TestFreshnessCountsARelativeRowAsPrunableWhateverTheCwd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proj", "sess.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{root, t.TempDir()} {
		t.Run(filepath.Base(cwd), func(t *testing.T) {
			t.Chdir(cwd)
			db := openTestLedger(t)
			insertRun(t, db, filepath.Join("proj", "sess.jsonl"))

			f, err := freshness(db, root)
			if err != nil {
				t.Fatal(err)
			}
			if f.prunable != 1 {
				t.Errorf("prunable = %d, want 1: a relative row can never match a walked path again (counts: %+v)", f.prunable, f)
			}
			if f.unexplained != 0 || f.missing != 0 || f.outsideRoot != 0 {
				t.Errorf("a relative row landed in a bucket that names the wrong cause (counts: %+v)", f)
			}
		})
	}
}

// Every one of these states looked like "gone from disk" in the first version
// of this classifier, and the out-of-root one is not rare: the root is a
// command argument, and the ledger deliberately holds runs from outside it.
// Against a narrowed root that misreported 221 real transcripts as prunable,
// and pointed them at a `loom report` that would never have touched them.
//
// The `unexplained` case is the same lesson one step further on: a row under the
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
	// A dangling symlink is walked (WalkDir lstats, so it is a plain .jsonl
	// entry) and then fails os.Stat, which is the only way to reach the
	// state-unknown count. Without it the totals assertion below is vacuous:
	// deleting the increment it guards leaves 1+0+0+0 == 1 passing.
	dangling := filepath.Join(root, "proj", "dangling.jsonl")
	if err := os.Symlink(filepath.Join(root, "proj", "nothing-here.jsonl"), dangling); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{current, journal, odd, elsewhere, gone, dangling} {
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
		{"unexplained", f.unexplained},
		{"unreadableOnDisk", f.unreadableOnDisk},
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
// still worth classifying. The version this replaces returned the zero struct
// without classifying anything, so this printed "0 transcripts on disk, 0
// ingested" against a ledger holding hundreds of rows. Absence read as
// confirmed-empty is the failure this project has already had once, in the
// memory store.
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

// rootIsAbsent is the only thing separating a new install from a walk that broke
// half way, and the error cannot tell them apart: WalkDir returns ENOENT for both
// "that root does not exist" and "a directory vanished while I was reading it".
// Tested directly because the second one is a race and cannot be staged, so no
// end-to-end test can reach it - which is also why the predicate has a name.
func TestRootIsAbsentAsksAboutTheRootNotTheError(t *testing.T) {
	root := t.TempDir()
	if rootIsAbsent(root) {
		t.Error("an existing root read as absent, so a mid-walk failure would be classified as a new install")
	}
	if !rootIsAbsent(filepath.Join(root, "never-created")) {
		t.Error("a missing root read as present, so a new install would be a hard error")
	}
}

// And the behaviour that predicate guards, from the other side: a root that
// exists but cannot be walked must fail loudly rather than be classified as a new
// install. Every earlier version of this code also errored here, so this test is
// not guarding against a regression that already happened - it pins the direction
// `rootIsAbsent` must not drift in, since the whole point of that predicate is to
// let some walk failures through and this is the half that must not be.
//
// Staged with an unreadable subdirectory because that is deterministic. It is
// EACCES rather than the ENOENT a mid-walk removal gives, so it does not exercise
// the ambiguity itself - that is what the predicate test above is for.
func TestFreshnessFailsLoudlyWhenAnExistingRootCannotBeWalked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the walk would not fail")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "proj")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored so TempDir's own cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	db := openTestLedger(t)
	insertRun(t, db, filepath.Join(blocked, "sess.jsonl"))

	if _, err := freshness(db, root); err == nil {
		t.Error("freshness returned no error on a root it could not walk: a partial file list classifies live transcripts as unexplained")
	}
}
