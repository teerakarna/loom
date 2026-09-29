package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azva-co/loom/internal/ingest"
	"github.com/azva-co/loom/internal/ledger"
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

func mustKnownRuns(t *testing.T, db *ledger.DB) []ledger.KnownRun {
	t.Helper()
	rows, err := db.KnownRuns()
	if err != nil {
		t.Fatal(err)
	}
	return rows
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
// A relative journal is deliberately this pass's problem and not the relative
// pass's. It qualifies on both counts, and the relative pass only deletes a row
// once the walk has produced a replacement - which for a journal never happens,
// since no walk returns one. Routed the other way it would sit in the ledger
// forever.
func TestPruneJournalRowsTakesJournalsWhereverTheyAre(t *testing.T) {
	db := openTestLedger(t)
	prune := []string{
		"/p/sess/subagents/workflows/wf-1/journal.jsonl", // orchestration journal, no usage
		"p/sess/subagents/workflows/wf-2/journal.jsonl",  // the same, stored relative
	}
	keep := []string{
		"/p/sess.jsonl",                                   // session transcript
		"/p/sess/subagents/agent-1.jsonl",                 // subagent transcript
		"/p/sess/subagents/workflows/wf-1/agent-2.jsonl",  // workflow's own subagent
		"/p/sess/subagents/workflows/wf-1/whatever.jsonl", // not a transcript, not a journal
		"p/sess.jsonl",                                    // relative, but a real transcript: not this pass's
	}
	for _, p := range append(append([]string{}, prune...), keep...) {
		insertRun(t, db, p)
	}

	if err := pruneJournalRows(db); err != nil {
		t.Fatal(err)
	}

	got := knownPaths(t, db)
	for _, p := range prune {
		if got[p] {
			t.Errorf("journal row %s survived the prune", p)
		}
	}
	for _, p := range keep {
		if !got[p] {
			t.Errorf("prune deleted %s, which it must never touch", p)
		}
	}
}

// The relative rows are the migration for this branch's own change: absolutising
// the root fixed the paths going in and did nothing for the ones already stored,
// which can no longer match a walked file under any root. Left in place, the next
// report inserts a second row for the same file and the ledger double-counts its
// cost forever.
//
// But a relative row is a full transcript with child rows hanging off it, so
// deleting one is only safe when the walk has actually produced its replacement.
// The version before this deleted every relative row unconditionally, before the
// walk, on the stated premise that the walk re-ingests each one in full. Three
// measured counterexamples, two of them staged below and the third in
// TestIngestAllKeepsChildRowsWhenMigratingARelativeRow: a narrowed root deleted
// rows it was never going to walk, an absent root deleted every one of them and
// then failed, and an already-migrated file lost its child rows permanently.
func TestSupersedeRelativeRowsOnlyTakesRowsTheWalkReplaces(t *testing.T) {
	db := openTestLedger(t)
	walked := []string{
		"/home/me/.claude/projects/p/sess.jsonl",
		"/home/me/.claude/projects/p/sess/subagents/agent-9.jsonl",
		// Two walked files with the same tail: the root was moved or copied,
		// and both halves are still under it.
		"/home/me/.claude/projects/dup/sess.jsonl",
		"/home/me/.claude/projects/old/dup/sess.jsonl",
	}
	superseded := []string{
		"p/sess/subagents/agent-9.jsonl",   // walked exactly once: safe to replace
		"./p/sess/subagents/agent-9.jsonl", // same file, unclean spelling
	}
	keep := []string{
		"p/gone.jsonl",   // nothing walked ends here: the walk cannot replace it
		"dup/sess.jsonl", // ends two walked paths: ambiguous, so not guessed at
	}
	for _, p := range append(append([]string{}, superseded...), keep...) {
		insertRun(t, db, p)
	}

	plan := planRelativeSupersessions(mustKnownRuns(t, db), walked, true)

	// Planning alone must not have touched the ledger - the delete waits for the
	// replacement to be read, which is what stops a failed read losing the row.
	before := knownPaths(t, db)
	for _, p := range append(append([]string{}, superseded...), keep...) {
		if !before[p] {
			t.Errorf("planning deleted %s; the delete belongs in commitSupersessions, after the read", p)
		}
	}

	// The plan is keyed on the replacement, which is also the forced re-read set,
	// and that is half the fix: without it NeedsIngest vetoes the read and the
	// replacement keeps whatever child rows it already had, which is none.
	//
	// Both relative spellings hang off that one key, and the value has to be a
	// slice for that reason. A plain map kept only the last, and since child rows
	// are first-seen-wins the survivor was usually the row holding the tool calls,
	// so the forced re-read attached nothing.
	replacement := "/home/me/.claude/projects/p/sess/subagents/agent-9.jsonl"
	if len(plan) != 1 || len(plan[replacement]) != 2 {
		t.Fatalf("plan = %v, want both relative spellings keyed to the one walked path that replaces them", plan)
	}
	for _, row := range plan[replacement] {
		if !strings.HasSuffix(filepath.ToSlash(filepath.Clean(row)), "p/sess/subagents/agent-9.jsonl") {
			t.Errorf("plan[%s] contains %q, which is not a spelling of that file", replacement, row)
		}
	}

	// Committing takes only the rows whose replacement actually read.
	read := map[string]ingest.RunSummary{replacement: {}}
	if err := commitSupersessions(db, plan, read); err != nil {
		t.Fatal(err)
	}
	got := knownPaths(t, db)
	for _, row := range plan[replacement] {
		if got[row] {
			t.Errorf("relative row %s survived even though its replacement was read", row)
		}
	}
	for _, p := range keep {
		if !got[p] {
			t.Errorf("deleted %s with no walked replacement for it: that row's history is simply gone", p)
		}
	}
}

// A walk over one project is not evidence about the corpus, so it does not get to
// supersede anything. The ambiguity guard counts candidates among walked paths, and
// narrowing the walk hides the second candidate: under the full root dup/sess.jsonl
// is correctly kept, and under <root>/old it would be confidently matched to the
// wrong file and deleted with its child rows. Same data loss this round exists to
// fix, reached through a narrowed root rather than an absent one.
func TestPlanRelativeSupersessionsNeedsTheWholeCorpus(t *testing.T) {
	db := openTestLedger(t)
	insertRun(t, db, "dup/sess.jsonl")
	narrowed := []string{"/home/me/.claude/projects/old/dup/sess.jsonl"}

	known := mustKnownRuns(t, db)
	plan := planRelativeSupersessions(known, narrowed, false)
	if len(plan) != 0 {
		t.Errorf("a narrowed walk planned %v; that match is only unambiguous because the other candidate was not walked", plan)
	}

	// Positive control, so the assertion above cannot pass just because nothing
	// ever matches: the same walk with the same row does match when it is claimed
	// to cover everything.
	plan = planRelativeSupersessions(known, narrowed, true)
	if len(plan) != 1 {
		t.Fatalf("plan = %v over the whole corpus, want the one match - the wholeCorpus check above proves nothing otherwise", plan)
	}
}

// A read that fails must leave the relative row alone. Deleting first and finding
// out afterwards left the child rows gone with nothing holding them and no way
// back, since the surviving absolute row's size already matched and NeedsIngest
// vetoed every later read.
func TestCommitSupersessionsSkipsRowsWhoseReplacementDidNotRead(t *testing.T) {
	db := openTestLedger(t)
	insertRun(t, db, "p/sess.jsonl")
	plan := map[string][]string{"/home/me/.claude/projects/p/sess.jsonl": {"p/sess.jsonl"}}

	if err := commitSupersessions(db, plan, map[string]ingest.RunSummary{}); err != nil {
		t.Fatal(err)
	}
	if !knownPaths(t, db)["p/sess.jsonl"] {
		t.Error("deleted the relative row although its replacement was never read: a stale figure is better than none")
	}
}

// transcriptWithChildRows writes a session transcript that produces tool_usage and
// compaction rows, not just a runs row. That distinction is the whole point of the
// test below: a fixture of bare `{}` lines ingests fine and proves nothing, because
// the data the migration was destroying lives in the child tables.
//
// The shapes are the ones ingest actually keys on, and getting them wrong fails
// silently. tool_usage comes from a `tool_result` block on a **user** line, matched
// back to a `tool_use` id seen earlier on an assistant line - a `tool_use` alone
// records nothing. A compaction needs a `system` line with subtype
// `compact_boundary`, a non-empty `uuid` to dedupe on, and a `compactMetadata`
// object; miss any of the three and parseCompaction returns nil.
func transcriptWithChildRows(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-09-01T10:00:00Z","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":100,"output_tokens":50},"content":[{"type":"tool_use","id":"t1","name":"Read","input":{}},{"type":"tool_use","id":"t2","name":"Bash","input":{}}]}}`,
		`{"type":"user","sessionId":"s1","timestamp":"2026-09-01T10:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"some file contents"},{"type":"tool_result","tool_use_id":"t2","content":"command output"}]}}`,
		`{"type":"system","subtype":"compact_boundary","sessionId":"s1","uuid":"u-boundary-1","timestamp":"2026-09-01T10:00:02Z","compactMetadata":{"trigger":"auto","preTokens":90000,"postTokens":20000,"durationMs":4200}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The migration deletes a row and relies on the walk to put it back, and nothing
// in this file could previously catch it getting that wrong: insertRun writes a
// bare runs row, so there were no child rows to lose. This runs the real thing
// end to end.
//
// The sequence is the one a real user hits, in order. An old binary ingested via a
// relative root, so the row and all of its tool_usage and compaction rows are
// stored under a relative path. Then a report absolutised the root, which inserted
// a *second* row for the same file and attached nothing to it - tool_use_id and
// boundary_uuid are globally unique and insert with ON CONFLICT DO NOTHING, so
// every child insert was a no-op against the rows the relative run already owned.
// Only then does the migration run.
//
// Deleting the relative row at that point takes the child rows with it, and
// NeedsIngest declines to re-read the file because the absolute row's size already
// matches. Measured against the version this replaces: tool_usage 2 -> 0,
// compactions 1 -> 0, unrecoverable across any number of later reports, with
// `loom context` printing "Nothing recorded yet" against a ledger holding the run.
func TestIngestAllKeepsChildRowsWhenMigratingARelativeRow(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	root := filepath.Join(home, ".claude", "projects")
	transcriptWithChildRows(t, filepath.Join(root, "proj", "sess.jsonl"))

	db := openTestLedger(t)
	// Stage one: the relative root, exactly as the old binary was run. The rows it
	// writes are relative, child rows included.
	rel := filepath.Join(".claude", "projects")
	if err := ingestAll(db, rel, rel, true); err != nil {
		t.Fatal(err)
	}
	before, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	if before.CompactionCount != 1 || toolCalls(before) != 2 {
		t.Fatalf("fixture produced no child rows to lose (tool calls %d, compactions %d): the rest of this test would pass vacuously",
			toolCalls(before), before.CompactionCount)
	}

	// Stage two: a report that absolutised the root but had no migration yet, so
	// it inserted a second row for the same file and attached nothing to it. This
	// state is not reachable by calling ingestAll - the migration would run first -
	// so it is built directly. It is the state on the disk of anyone who ran a
	// build from that window, and it is what makes the deletion lossy: without it
	// the walk re-reads the file and the child rows come back on their own.
	abs := filepath.Join(root, "proj", "sess.jsonl")
	fi, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: abs, Kind: "session", SizeBytes: fi.Size()}); err != nil {
		t.Fatal(err)
	}

	// Stage three: the migration, twice - the second run is the check that a loss
	// here is permanent rather than repaired on the next report.
	for range 2 {
		if err := ingestAll(db, root, root, true); err != nil {
			t.Fatal(err)
		}
	}

	after, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	if toolCalls(after) != 2 {
		t.Errorf("tool_usage calls = %d, want 2: the migration deleted them and nothing re-read the file", toolCalls(after))
	}
	if after.CompactionCount != 1 {
		t.Errorf("compactions = %d, want 1: same loss, and asset_usage goes with it - which makes propose offer live assets for retirement as never-used", after.CompactionCount)
	}
	// And the visible half: one file, one row, under its absolute path.
	got := knownPaths(t, db)
	want := filepath.Join(root, "proj", "sess.jsonl")
	if len(got) != 1 || !got[want] {
		t.Errorf("runs = %v, want exactly %s", got, want)
	}
}

// The other two ways "delete it, the walk will bring it back" was wrong, and the
// reason the relative prune sits after the walk rather than before it. Both are
// about a walk that was never going to produce the replacement: one narrowed to a
// single project, one that could not run at all. Before the reorder each deleted
// every relative row in the ledger regardless, and the absent-root case then failed
// on the lstat with nothing left to restore from - which also broke a promise
// `loom status` makes two lines from where it counts these, that an unreachable row
// means "not data loss".
func TestIngestAllKeepsRelativeRowsNoWalkCanReplace(t *testing.T) {
	for _, c := range []struct {
		name      string
		rootUnder func(root string) string
		wantErr   bool
	}{
		{"narrowed-root", func(root string) string { return filepath.Join(root, "p1") }, false},
		{"absent-root", func(root string) string { return filepath.Join(root, "never-created") }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Chdir(home)
			root := filepath.Join(home, ".claude", "projects")
			transcriptWithChildRows(t, filepath.Join(root, "p1", "a.jsonl"))
			transcriptWithChildRows(t, filepath.Join(root, "p2", "b.jsonl"))

			db := openTestLedger(t)
			if err := ingestAll(db, filepath.Join(".claude", "projects"), filepath.Join(".claude", "projects"), true); err != nil {
				t.Fatal(err)
			}
			if got := len(knownPaths(t, db)); got != 2 {
				t.Fatalf("setup ingested %d relative rows, want 2", got)
			}

			// wholeCorpus is passed true even though the walk plainly is not the
			// whole corpus. Deliberate: the gate added for the ambiguity case would
			// otherwise make this pass for a second reason, and the ordering fix
			// this test exists for would stop being what holds it up.
			narrowed := c.rootUnder(root)
			err := ingestAll(db, narrowed, narrowed, true)
			if c.wantErr && err == nil {
				t.Error("a root that does not exist returned no error, so the prune below is not the interesting part any more")
			}
			if !c.wantErr && err != nil {
				t.Fatal(err)
			}

			// p2's row is the one at stake either way: no walk here reaches it,
			// so deleting it destroys the only record of that run. Keyed on the
			// path as walked - the row records the root it was found under, not a
			// path relative to it.
			got := knownPaths(t, db)
			if !got[filepath.Join(".claude", "projects", "p2", "b.jsonl")] {
				t.Errorf("p2's relative row was deleted with nothing to replace it (rows: %v)", got)
			}
		})
	}
}

// A lane names the project directory a transcript belongs to, so it has to be
// derived from the projects root and not from whatever root the invocation walked.
// `loom report <root>/<one-project>` used to recompute the lane relative to that
// narrower root, which for a nested subagent transcript is the session directory:
// the lane became a session UUID. Measured on a real ledger, one such report moved
// 82 of 85 runs off their project onto two UUIDs, and NeedsIngest then vetoed every
// later read that could have put them back.
//
// The nesting is the case that matters, so the fixture has both: a transcript
// directly in the project directory (which recomputed to an empty lane, the
// separate defect the ledger's COALESCE now absorbs) and one under subagents/,
// which recomputed to a plausible-looking wrong value that no COALESCE can catch.
func TestIngestAllDerivesLanesFromTheProjectsRootNotTheWalkedRoot(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	root := filepath.Join(home, ".claude", "projects")
	proj := filepath.Join(root, "-h-u-proj")
	transcriptWithChildRows(t, filepath.Join(proj, "sess.jsonl"))
	transcriptWithChildRows(t, filepath.Join(proj, "sess", "subagents", "agent-1.jsonl"))

	db := openTestLedger(t)
	if err := ingestAll(db, root, root, true); err != nil {
		t.Fatal(err)
	}
	assertOneLane(t, db, "-h-u-proj", "after a full-root report")

	// Now the narrowed walk. Both files have to look grown or NeedsIngest skips
	// them and the assertion passes without the code being exercised - which is
	// also what a live session does between two reports, so it is the realistic
	// case rather than a contrivance.
	for _, p := range []string{
		filepath.Join(proj, "sess.jsonl"),
		filepath.Join(proj, "sess", "subagents", "agent-1.jsonl"),
	} {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{}\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if err := ingestAll(db, proj, root, false); err != nil {
		t.Fatal(err)
	}
	assertOneLane(t, db, "-h-u-proj", "after a report narrowed to that project")
}

// assertOneLane checks that every run in the ledger is attributed to want, via
// the by-lane breakdown a user actually reads rather than a private query.
func assertOneLane(t *testing.T, db *ledger.DB, want, when string) {
	t.Helper()
	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalRuns != 2 {
		t.Fatalf("runs = %d %s, want the 2 the fixture wrote: the lane assertion would not measure anything", s.TotalRuns, when)
	}
	if len(s.ByLane) != 1 || s.ByLane[0].Lane != want || s.ByLane[0].Runs != 2 {
		t.Errorf("lanes %s = %+v, want all 2 runs under %q", when, s.ByLane, want)
	}
}

// toolCalls sums Occupancy's per-tool call counts. Occupancy joins tool_usage to
// runs, so this counts what a user can actually see - a child row orphaned from
// its run would not appear, which is the right measure here.
func toolCalls(r ledger.OccupancyReport) int {
	n := 0
	for _, t := range r.ByTool {
		n += t.Calls
	}
	return n
}

// A relative row must classify as prunable, and it must do so without asking the
// filesystem anything: the cwd a `loom status` happens to run from is not the one
// the row was written against, so a stat would answer a different question each
// time. Staged so the row's path resolves to a real file under the root from one
// cwd and to nothing from the other - which without the check lands it in a
// different bucket each time, `unexplained` from the root and `missing` from
// anywhere else. ("Current" is not one of the outcomes: a relative row never
// matches a walked path, so it cannot reach the on-disk arm of the classifier at
// all, and an earlier version of this comment claiming otherwise was wrong.)
func TestFreshnessCountsARelativeRowAsPrunableWhateverTheCwd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proj", "sess.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, cwd string }{
		{"cwd-is-root", root},
		{"cwd-elsewhere", t.TempDir()},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(c.cwd)
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
			// The visible half, and the reason the row is worth reporting at
			// all: the file is right there and counted as never ingested, so
			// the ledger holds a run for it and cannot say so.
			if f.onDisk != 1 || f.unseen != 1 {
				t.Errorf("onDisk = %d, unseen = %d, want 1 and 1 (counts: %+v)", f.onDisk, f.unseen, f)
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

// The default projects root is the lane root for any walk inside it, and for a
// walk outside it there is no lane root at all - the empty string, meaning
// attribute nothing.
//
// Both halves are regressions that happened. Deriving lanes from the walked root
// made a narrowed report file 82 of 85 runs under session UUIDs. Substituting the
// default unconditionally lost every lane for a root that is not this machine's
// own. Returning root for those - the third version - was the first defect again
// wearing the second's clothes, because a walk narrowed *inside* an alternate root
// is indistinguishable from a walk of one, so the backup and ancestor cases below
// pin "" rather than root: no lane is recoverable later, a wrong one is not.
func TestLaneRootForOnlySubstitutesTheDefaultForAWalkInsideIt(t *testing.T) {
	const def = "/home/me/.claude/projects"
	for _, c := range []struct {
		name, root, want string
	}{
		{"the default itself", def, def},
		{"narrowed to one project", def + "/proj-a", def},
		{"narrowed to a session inside a project", def + "/proj-a/sess", def},
		{"a restored backup elsewhere", "/mnt/backup/projects", ""},
		{"a sibling with the same prefix text", def + "-old", ""},
		{"an ancestor of the default", "/home/me/.claude", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := laneRootFor(def, c.root); got != c.want {
				t.Errorf("laneRootFor(%q, %q) = %q, want %q", def, c.root, got, c.want)
			}
		})
	}
}

// And the consequence that makes returning "" safe rather than merely honest: no
// lane is derived at all on such a walk, for any shape of path. The relative case
// is the one that matters - LaneFromPath alone answers "relative" for it, since
// an empty root cleans to "." and Rel succeeds, so this property belongs to
// laneOf and not to LaneFromPath.
func TestAnEmptyLaneRootDerivesNoLane(t *testing.T) {
	for _, p := range []string{
		"/mnt/backup/projects/proj-a/sess.jsonl",
		"relative/proj-a/sess.jsonl",
		"sess.jsonl",
	} {
		if got := laneOf("", p); got != "" {
			t.Errorf("laneOf(%q, %q) = %q, want the empty lane", "", p, got)
		}
	}
	// The positive control: a real lane root still reads the lane, so the test
	// above is not passing because laneOf returns "" for everything.
	if got := laneOf("/home/me/.claude/projects", "/home/me/.claude/projects/proj-a/sess.jsonl"); got != "proj-a" {
		t.Errorf("laneOf with a real root = %q, want proj-a", got)
	}
}
