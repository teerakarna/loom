package ledger

import (
	"database/sql"
	"errors"
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

	var existing sql.NullString
	err := d.sql.QueryRow(`SELECT unscanned_since FROM memory_root_scan WHERE id = 1`).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, err
	}
	if existing.Valid && existing.String != "" {
		since, err := parseTime(existing.String)
		if err != nil {
			return time.Time{}, false, err
		}
		return since, true, nil
	}

	// First failure since a working pass (or the row never existed yet) -
	// this pass is the streak's start.
	if _, err := d.sql.Exec(`
		INSERT INTO memory_root_scan (id, unscanned_since) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET unscanned_since = excluded.unscanned_since`,
		formatTime(now)); err != nil {
		return time.Time{}, false, err
	}
	return now, true, nil
}
