package ledger

import "testing"

// TestOpenUsesWALMode is the regression test for the data-safety half of
// docs/design.md's Verification section, "Latency": kill the loom serve
// process mid-session and confirm the session is unaffected. SQLite's own
// crash-recovery guarantee is what makes that true - an abrupt process
// death never corrupts the database or leaves an uncommitted write
// half-visible - but that guarantee only holds in WAL journal mode, not the
// default rollback-journal mode. Loom's own responsibility, and the only
// part of this claim a unit test can verify without re-testing SQLite's own
// well-covered crash-recovery implementation, is that Open actually turns
// WAL mode on. A prior version of this test tried to simulate the crash
// itself (two in-process connections) and found it unreliable: Go's
// database/sql connection pooling does not release a transaction's lock
// just because the *sql.DB it came from was closed, so the simulation
// deadlocked for a reason a real process death never would - testing an
// artifact of the simulation, not the property itself.
func TestOpenUsesWALMode(t *testing.T) {
	db := openTestDB(t)

	var mode string
	if err := db.sql.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal - this is the mechanism that makes a killed loom serve process safe for the ledger", mode)
	}
}
