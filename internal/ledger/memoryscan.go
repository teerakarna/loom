package ledger

import (
	"database/sql"
	"time"
)

// RecordRootScanCoverage updates the ledger's memory of whether
// <home>/.claude/projects was successfully enumerated this pass, and
// reports how long it has been in an unbroken run of scan failures, if it
// currently is one - see propose.needsProtection and issue #76.
//
// scanned=true (the root was enumerated fine this pass, whatever it found)
// clears any streak: today it works, and whatever happened before does not
// matter. Returns the zero time and ok=false.
//
// scanned=false only records the streak's start the first time it sees
// this after a working pass; a second, third, nth consecutive failure
// leaves the original timestamp alone, so the caller can tell how long the
// streak has actually run for, not just that it is currently failing.
// Returns that start time and ok=true.
func (d *DB) RecordRootScanCoverage(scanned bool, now time.Time) (time.Time, bool, error) {
	if scanned {
		_, err := d.sql.Exec(`
			INSERT INTO memory_root_scan (id, unscanned_since) VALUES (1, NULL)
			ON CONFLICT(id) DO UPDATE SET unscanned_since = NULL`)
		return time.Time{}, false, err
	}

	// One atomic statement, not a separate SELECT-then-write: two Store
	// calls racing right as a streak starts (the CLI and the MCP server, or
	// two overlapping MCP calls, against the same ledger) could otherwise
	// both see no existing row and each write their own now, and whichever
	// finished last would silently overwrite the true first-failure time
	// the tolerance window is measured from (found by code review, before
	// this shipped). COALESCE keeps the existing value across every
	// consecutive failure and only takes the new one the first time.
	var existing sql.NullString
	err := d.sql.QueryRow(`
		INSERT INTO memory_root_scan (id, unscanned_since) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET
			unscanned_since = COALESCE(memory_root_scan.unscanned_since, excluded.unscanned_since)
		RETURNING unscanned_since`,
		formatTime(now)).Scan(&existing)
	if err != nil {
		return time.Time{}, false, err
	}
	since, err := parseTime(existing.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return since, true, nil
}
