package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

// TestFreshnessBehindVersionIsNotStale is the regression test for issue #89:
// a row unchanged in size but stamped below CurrentFeatureVersion must be
// counted separately from a row whose size actually grew, since "grown
// since" is a false reason for a file that has not changed at all.
func TestFreshnessBehindVersionIsNotStale(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "current.jsonl")
	behind := filepath.Join(root, "behind.jsonl")
	grown := filepath.Join(root, "grown.jsonl")
	for _, p := range []string{current, behind, grown} {
		if err := os.WriteFile(p, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	size := fi.Size()

	db := openTestLedger(t)

	if err := db.InsertRun(ledger.RunRecord{Path: current, Kind: "session", SizeBytes: size}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: behind, Kind: "session", SizeBytes: size}); err != nil {
		t.Fatal(err)
	}
	if err := db.InvalidateRun(behind); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: grown, Kind: "session", SizeBytes: size - 1}); err != nil {
		t.Fatal(err)
	}

	f, err := freshness(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if f.current != 1 {
		t.Errorf("current = %d, want 1 (only the unchanged, up-to-version row)", f.current)
	}
	if f.behindVersion != 1 {
		t.Errorf("behindVersion = %d, want 1 (the unchanged row stamped below CurrentFeatureVersion)", f.behindVersion)
	}
	if f.stale != 1 {
		t.Errorf("stale = %d, want 1 (only the row whose size actually grew)", f.stale)
	}
}
