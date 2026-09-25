package ledger

import (
	"strconv"
	"testing"

	"github.com/teerakarna/loom/internal/ingest"
)

// Child rows are the risk here: there is no ON DELETE CASCADE and foreign
// keys are not enabled on this connection, so a delete that only touches
// `runs` leaves tool_usage/compactions/asset_usage rows pointing at a dead
// id. Not because the id gets reused - it is AUTOINCREMENT - but because
// tool_use_id and boundary_uuid are globally unique and first-seen-wins, so a
// leftover row keeps an id claimed on behalf of a run that no longer exists
// and a resumed session's replay of the same event is silently dropped.
func TestDeleteRunsRemovesChildRows(t *testing.T) {
	db := openTestDB(t)
	id := insertTestRun(t, db, "phantom.jsonl")
	keepID := insertTestRun(t, db, "keep.jsonl")

	for _, target := range []int64{id, keepID} {
		if err := db.ReplaceToolUsage(target, []ingest.ToolUsageEvent{
			{ToolUseID: "t-" + strconv.FormatInt(target, 10), ToolName: "Read", ResultBytes: 10},
		}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := db.DeleteRuns([]string{"phantom.jsonl", "never-ingested.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	// A path that was never in the ledger is not an error and is not counted.
	if n != 1 {
		t.Errorf("DeleteRuns deleted %d runs, want 1", n)
	}

	var runs, orphans, kept int
	if err := db.sql.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT count(*) FROM tool_usage WHERE run_id = ?`, id).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT count(*) FROM tool_usage WHERE run_id = ?`, keepID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || orphans != 0 || kept != 1 {
		t.Errorf("after delete: runs=%d orphaned tool_usage=%d kept tool_usage=%d; want 1, 0, 1", runs, orphans, kept)
	}
}

// DeleteRuns must be a no-op on an empty list rather than deleting everything,
// which is what a naive "WHERE path IN ()" would risk.
func TestDeleteRunsEmptyIsNoOp(t *testing.T) {
	db := openTestDB(t)
	insertTestRun(t, db, "keep.jsonl")

	if n, err := db.DeleteRuns(nil); err != nil || n != 0 {
		t.Fatalf("DeleteRuns(nil) = %d, %v; want 0, nil", n, err)
	}
	var runs int
	if err := db.sql.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Errorf("runs = %d after DeleteRuns(nil), want 1", runs)
	}
}
