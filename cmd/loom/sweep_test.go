package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

func TestPrintSweepSplitsTiersAndGroupsSameAdvice(t *testing.T) {
	items := []propose.SweepItem{
		{ID: 1, Tier: propose.TierSafe, Summary: "s1", Line: "- [a](a.md)"},
		{ID: 2, Tier: propose.TierDecide, Summary: "d2", BottomLine: "same", Options: []string{"x"}},
		{ID: 3, Tier: propose.TierDecide, Summary: "d3", BottomLine: "same", Options: []string{"x"}},
		{ID: 4, Tier: propose.TierDecide, Summary: "d4", BottomLine: "other", Options: []string{"y"}},
	}
	var b bytes.Buffer
	printSweep(&b, items)
	out := b.String()

	if !strings.Contains(out, "Safe to sweep: 1") || !strings.Contains(out, "Needs a decision: 3") {
		t.Errorf("tier counts missing:\n%s", out)
	}
	if n := strings.Count(out, "bottom line: same"); n != 1 {
		t.Errorf("identical advice printed %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "bottom line: other") {
		t.Errorf("different advice must stay separate:\n%s", out)
	}
	if strings.Index(out, "#1") > strings.Index(out, "Needs a decision") {
		t.Errorf("safe item printed in the wrong section:\n%s", out)
	}
}

func TestPrintSweepSaysWhenNothingIsSafe(t *testing.T) {
	var b bytes.Buffer
	printSweep(&b, []propose.SweepItem{{ID: 2, Tier: propose.TierDecide, BottomLine: "b", Options: []string{"x"}}})
	if !strings.Contains(b.String(), "nothing passed its checks") || strings.Contains(b.String(), "sweep.sh") {
		t.Errorf("no safe items should mean no script instructions:\n%s", b.String())
	}
}

func TestSweepRejectsUnknownFlag(t *testing.T) {
	if err := runSweep(nil, []string{"--bogus"}); err == nil {
		t.Fatal("want a usage error")
	}
}

// Sweep prints and never writes: same read-only tree as the ingest/propose
// containment test, so a write attempt fails loudly as a permission error.
func TestSweepNeverWritesOutsideTheLedger(t *testing.T) {
	root, before := buildReadOnlyFixtureTree(t)
	t.Setenv("HOME", root)

	db, err := ledger.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	for _, args := range [][]string{nil, {"--script"}} {
		if err := runSweep(db, args); err != nil {
			t.Fatalf("runSweep %v against a read-only tree: %v", args, err)
		}
	}

	after := hashTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed", p)
		}
	}
}
