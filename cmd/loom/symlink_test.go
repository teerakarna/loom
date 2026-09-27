package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

// TestResolveRootFollowsASymlink is the direct unit test for issue #88's
// underlying gap: two absolute spellings of the same real directory - a
// symlinked ~/.claude, say - used to compare unequal under plain
// filepath.Abs. resolveRoot must make them compare equal.
func TestResolveRootFollowsASymlink(t *testing.T) {
	underlying := t.TempDir()
	link := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(underlying, link); err != nil {
		t.Fatal(err)
	}

	viaLink, err := resolveRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	viaReal, err := resolveRoot(underlying)
	if err != nil {
		t.Fatal(err)
	}
	if viaLink != viaReal {
		t.Errorf("resolveRoot(%q) = %q, resolveRoot(%q) = %q, want them equal", link, viaLink, underlying, viaReal)
	}
}

// TestResolveRootOnAMissingPathFallsBackToAbs confirms the one case
// resolveRoot must not treat as an error: a projects root that has never
// existed is a normal new-install state, and EvalSymlinks refuses to
// resolve a path it cannot stat, unlike Abs.
func TestResolveRootOnAMissingPathFallsBackToAbs(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created", "projects")
	got, err := resolveRoot(missing)
	if err != nil {
		t.Fatalf("resolveRoot on a missing path = %v, want (abs path, nil)", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveRoot(%q) = %q, want an absolute path", missing, got)
	}
}

// realFile writes a real file at path (creating its parent directories) and
// returns it - most of these tests need soleWalkedPathSameFileAs to stat a
// real file, unlike the relative-row migration's own tests, which are pure
// string matching and use fictional, non-existent paths throughout.
func realFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSoleWalkedPathSameFileAsFindsAnIdenticalFileUnderAnotherSpelling(t *testing.T) {
	home := t.TempDir()
	underlying := realFile(t, filepath.Join(home, "real", "sess.jsonl"))
	link := filepath.Join(home, "linked", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(underlying, link); err != nil {
		t.Fatal(err)
	}

	got, ok := soleWalkedPathSameFileAs([]string{link}, underlying)
	if !ok || got != link {
		t.Errorf("soleWalkedPathSameFileAs(%v, %q) = (%q, %v), want (%q, true)", []string{link}, underlying, got, ok, link)
	}
}

func TestSoleWalkedPathSameFileAsRejectsAmbiguity(t *testing.T) {
	home := t.TempDir()
	underlying := realFile(t, filepath.Join(home, "real", "sess.jsonl"))
	linkA := filepath.Join(home, "a", "sess.jsonl")
	linkB := filepath.Join(home, "b", "sess.jsonl")
	for _, l := range []string{linkA, linkB} {
		if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(underlying, l); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := soleWalkedPathSameFileAs([]string{linkA, linkB}, underlying); ok {
		t.Error("two walked paths both resolve to the same file - ambiguous, must not guess")
	}
}

func TestSoleWalkedPathSameFileAsRejectsADifferentFile(t *testing.T) {
	home := t.TempDir()
	underlying := realFile(t, filepath.Join(home, "real", "sess.jsonl"))
	other := realFile(t, filepath.Join(home, "other", "sess.jsonl")) // same name, different file

	if _, ok := soleWalkedPathSameFileAs([]string{other}, underlying); ok {
		t.Error("same filename, different real file - must not match on identity it does not have")
	}
}

// TestPlanSymlinkSupersessionsOnlyTakesRowsTheWalkReplaces mirrors
// TestSupersedeRelativeRowsOnlyTakesRowsTheWalkReplaces for issue #88's
// absolute-but-differently-spelled rows: a row the walk did not itself
// reproduce, but which resolves to exactly one walked file, is superseded;
// a row already walked under its own spelling, or one with no match at
// all, is not.
func TestPlanSymlinkSupersessionsOnlyTakesRowsTheWalkReplaces(t *testing.T) {
	home := t.TempDir()
	underlying := realFile(t, filepath.Join(home, "real", "proj", "sess.jsonl"))
	linked := filepath.Join(home, "linked", "proj", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(underlying, linked); err != nil {
		t.Fatal(err)
	}
	untouched := realFile(t, filepath.Join(home, "real", "proj", "other.jsonl"))

	db := openTestLedger(t)
	insertRun(t, db, underlying) // stored under the old spelling: should be superseded
	insertRun(t, db, untouched)  // walked under its own spelling: not superseded

	walked := []string{linked, untouched}
	plan, err := planSymlinkSupersessions(db, walked, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || len(plan[linked]) != 1 || plan[linked][0] != underlying {
		t.Fatalf("plan = %v, want {%q: [%q]}", plan, linked, underlying)
	}
}

// TestPlanSymlinkSupersessionsNeedsTheWholeCorpus mirrors
// TestPlanRelativeSupersessionsNeedsTheWholeCorpus: a narrowed walk is not
// evidence about the corpus, so it does not get to supersede anything, even
// when the match would otherwise be unambiguous.
func TestPlanSymlinkSupersessionsNeedsTheWholeCorpus(t *testing.T) {
	home := t.TempDir()
	underlying := realFile(t, filepath.Join(home, "real", "sess.jsonl"))
	linked := filepath.Join(home, "linked", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(underlying, linked); err != nil {
		t.Fatal(err)
	}

	db := openTestLedger(t)
	insertRun(t, db, underlying)

	plan, err := planSymlinkSupersessions(db, []string{linked}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Errorf("a narrowed walk planned %v; wholeCorpus=false must suppress this entirely", plan)
	}

	plan, err = planSymlinkSupersessions(db, []string{linked}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 {
		t.Fatalf("plan = %v over the whole corpus, want the one match", plan)
	}
}

// TestIngestAllKeepsChildRowsWhenMigratingASymlinkedRow is
// TestIngestAllKeepsChildRowsWhenMigratingARelativeRow's twin, and the real
// end-to-end reproduction of issue #88: a symlinked ~/.claude means the
// default root and an explicitly-given resolved root are two different
// absolute strings for the same real files, and without this migration the
// second report ever run against the resolved spelling inserts a second row
// with none of the first row's child data attached to it.
func TestIngestAllKeepsChildRowsWhenMigratingASymlinkedRow(t *testing.T) {
	underlying := t.TempDir()
	home := t.TempDir()
	link := filepath.Join(home, ".claude")
	if err := os.Symlink(underlying, link); err != nil {
		t.Fatal(err)
	}
	rootViaLink := filepath.Join(link, "projects")
	rootViaReal := filepath.Join(underlying, "projects")
	transcriptWithChildRows(t, filepath.Join(rootViaReal, "proj", "sess.jsonl"))

	db := openTestLedger(t)
	// Stage one: ingested via the symlinked spelling, exactly as a default
	// `loom report` would with a symlinked ~/.claude.
	if err := ingestAll(db, rootViaLink, rootViaLink, true); err != nil {
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

	// Stage two: the state on disk of anyone who ran a pre-#88 build and
	// then happened to invoke `loom report` with the resolved spelling
	// instead (an explicit path, a differently-configured shell, a second
	// machine mounting the same real directory without the symlink) - not
	// reachable by calling ingestAll, since the migration would run first,
	// so built directly.
	absViaReal := filepath.Join(rootViaReal, "proj", "sess.jsonl")
	fi, err := os.Stat(absViaReal)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: absViaReal, Kind: "session", SizeBytes: fi.Size()}); err != nil {
		t.Fatal(err)
	}

	// Stage three: the migration, twice - the second run checks the loss
	// is not merely deferred to whichever spelling loses the coin toss.
	for range 2 {
		if err := ingestAll(db, rootViaReal, rootViaReal, true); err != nil {
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
		t.Errorf("compactions = %d, want 1", after.CompactionCount)
	}
	got := knownPaths(t, db)
	if len(got) != 1 || !got[absViaReal] {
		t.Errorf("runs = %v, want exactly %s - one row, under the spelling this walk actually used", got, absViaReal)
	}
}
