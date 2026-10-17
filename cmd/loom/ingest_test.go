package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

// scratchHome points HOME at a temp dir holding one real-shaped transcript, so
// neither command can touch the real ~/.loom or ~/.claude.
func scratchHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "synthetic-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", "-scratch-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), src, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func ledgerRuns(t *testing.T) int {
	t.Helper()
	p, err := defaultLedgerPath()
	if err != nil {
		t.Fatal(err)
	}
	db, err := ledger.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	return s.TotalRuns
}

func TestRunIngestIsSilentAndFillsTheLedger(t *testing.T) {
	scratchHome(t)
	out, err := captureStdout(t, func() error { return runIngest(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing: a hook's output lands in the session", out)
	}
	if n := ledgerRuns(t); n == 0 {
		t.Error("ledger has no runs after ingest")
	}
}

// The counterpoint: report over the same corpus must still print.
func TestRunReportStillPrintsAfterTheRefactor(t *testing.T) {
	scratchHome(t)
	out, err := captureStdout(t, func() error { return runReport(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Runs ingested:") {
		t.Errorf("report stdout = %q, want the report", out)
	}
}

func TestRunIngestRejectsLaneAndExtraArgs(t *testing.T) {
	for _, args := range [][]string{{"--lane", "x"}, {"/a", "/b"}} {
		err := runIngest(args)
		if err == nil || !strings.Contains(err.Error(), "usage: loom ingest") {
			t.Errorf("runIngest(%v) err = %v, want the ingest usage string", args, err)
		}
	}
}
