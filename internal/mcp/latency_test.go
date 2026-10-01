package mcp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
)

// TestServerColdStartUnder20ms is the regression test for docs/design.md's
// Verification section, "Latency": cold start under 20ms. NewServer is what
// a session actually waits on when the plugin loads - everything in it
// (tool registration, the Implementation struct) is in-memory construction,
// no I/O, so 20ms is generous rather than tight; a regression here would
// mean something expensive snuck into server construction itself, not into
// a handler that only runs when a tool is actually called.
func TestServerColdStartUnder20ms(t *testing.T) {
	db, err := ledger.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	home := t.TempDir()

	start := time.Now()
	_ = NewServer(db, home)
	elapsed := time.Since(start)

	const ceiling = 20 * time.Millisecond
	if elapsed > ceiling {
		t.Errorf("NewServer took %s, want under %s", elapsed, ceiling)
	}
}
