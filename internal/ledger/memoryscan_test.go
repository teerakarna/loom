package ledger

import (
	"testing"
	"time"
)

func TestRecordRootScanCoverage_ScannedClearsAnyStreak(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	if _, ok, err := db.RecordRootScanCoverage(false, now); err != nil || !ok {
		t.Fatalf("first failure = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if _, ok, err := db.RecordRootScanCoverage(true, now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("scanned = (ok=%v, err=%v), want (false, nil) - a successful pass clears the streak", ok, err)
	}
	// The streak really is gone, not just reporting ok=false once: a later
	// failure must start counting from scratch, not from the earlier streak.
	since, ok, err := db.RecordRootScanCoverage(false, now.Add(2*time.Hour))
	if err != nil || !ok {
		t.Fatalf("second failure = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if !since.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("since = %v, want %v - a new streak starting after the cleared one", since, now.Add(2*time.Hour))
	}
}

func TestRecordRootScanCoverage_ConsecutiveFailuresKeepTheOriginalStart(t *testing.T) {
	db := openTestDB(t)
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	since, ok, err := db.RecordRootScanCoverage(false, start)
	if err != nil || !ok || !since.Equal(start) {
		t.Fatalf("got (since=%v, ok=%v, err=%v), want (%v, true, nil)", since, ok, err, start)
	}
	// Ten more consecutive failures, each pretending to be a day later -
	// every one must report the ORIGINAL start, not its own timestamp.
	for i := 1; i <= 10; i++ {
		since, ok, err := db.RecordRootScanCoverage(false, start.Add(time.Duration(i)*24*time.Hour))
		if err != nil || !ok {
			t.Fatalf("failure %d = (ok=%v, err=%v), want (true, nil)", i, ok, err)
		}
		if !since.Equal(start) {
			t.Errorf("failure %d: since = %v, want the original start %v, not this pass's own time", i, since, start)
		}
	}
}

func TestRecordRootScanCoverage_NeverFailedReportsNotOK(t *testing.T) {
	db := openTestDB(t)
	if _, ok, err := db.RecordRootScanCoverage(true, time.Now()); err != nil || ok {
		t.Fatalf("got (ok=%v, err=%v), want (false, nil) on a ledger that never recorded a failure", ok, err)
	}
}
