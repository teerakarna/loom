package ledger

import (
	"testing"

	"github.com/teerakarna/loom/internal/ingest"
)

// insertTestRun inserts a minimal run and returns its id, for tests that
// only need a valid run_id to attach tool_usage/compactions rows to.
func insertTestRun(t *testing.T, db *DB, path string) int64 {
	t.Helper()
	if err := db.InsertRun(RunRecord{Path: path, Kind: "session", Model: "claude-sonnet-5"}); err != nil {
		t.Fatal(err)
	}
	id, err := db.RunIDByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestReplaceToolUsage_ReplacesNotAccumulates matches the contract InsertRun
// keeps for the runs table: usage is recomputed from a full re-read of the
// file on every ingest, so a second ReplaceToolUsage call must overwrite the
// first, never accumulate on top of it.
func TestReplaceToolUsage_ReplacesNotAccumulates(t *testing.T) {
	db := openTestDB(t)
	id := insertTestRun(t, db, "run-a.jsonl")

	if err := db.ReplaceToolUsage(id, map[string]ingest.ToolUsageStat{
		"Read": {Calls: 1, ResultBytes: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceToolUsage(id, map[string]ingest.ToolUsageStat{
		"Read": {Calls: 3, ResultBytes: 900},
		"Bash": {Calls: 1, ResultBytes: 50},
	}); err != nil {
		t.Fatal(err)
	}

	occ, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	byTool := map[string]ToolOutputRow{}
	for _, r := range occ.ByTool {
		byTool[r.ToolName] = r
	}
	if len(byTool) != 2 {
		t.Fatalf("ByTool = %+v, want exactly 2 rows (the second call's set, not a union with the first)", occ.ByTool)
	}
	if r := byTool["Read"]; r.Calls != 3 || r.ResultBytes != 900 {
		t.Errorf("Read = %+v, want {Calls:3 ResultBytes:900}", r)
	}
	if r := byTool["Bash"]; r.Calls != 1 || r.ResultBytes != 50 {
		t.Errorf("Bash = %+v, want {Calls:1 ResultBytes:50}", r)
	}
}

// TestInsertCompactions_DedupesAcrossResumedSessions is the regression shape
// for the 1.7M-token over-count: a resumed session replays its prior
// compaction history verbatim, uuid included, into a second transcript with
// its own run_id. Inserting the same uuid under a second run must not
// double the count.
func TestInsertCompactions_DedupesAcrossResumedSessions(t *testing.T) {
	db := openTestDB(t)
	original := insertTestRun(t, db, "original-session.jsonl")
	resumed := insertTestRun(t, db, "resumed-session.jsonl")

	shared := ingest.CompactionEvent{
		UUID: "shared-boundary", Trigger: "manual",
		PreTokens: 1000, PostTokens: 100, CumulativeDroppedTokens: 900, DurationMs: 1000,
	}
	if err := db.InsertCompactions(original, []ingest.CompactionEvent{shared}); err != nil {
		t.Fatal(err)
	}
	// The resumed session's own file replays the same event, plus one
	// genuinely new compaction that happened after resuming.
	newEvent := ingest.CompactionEvent{
		UUID: "new-after-resume", Trigger: "manual",
		PreTokens: 500, PostTokens: 50, CumulativeDroppedTokens: 1350, DurationMs: 500,
	}
	if err := db.InsertCompactions(resumed, []ingest.CompactionEvent{shared, newEvent}); err != nil {
		t.Fatal(err)
	}

	occ, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	if occ.CompactionCount != 2 {
		t.Fatalf("CompactionCount = %d, want 2 (shared event counted once, plus the genuinely new one)", occ.CompactionCount)
	}
	// Dropped tokens: (1000-100) + (500-50) = 900 + 450 = 1350. If the shared
	// event had been double-counted this would be 900 too many.
	if want := int64(1350); occ.CompactionDroppedTokens != want {
		t.Errorf("CompactionDroppedTokens = %d, want %d", occ.CompactionDroppedTokens, want)
	}

	// Re-inserting the same run's own events again (a re-ingest after the
	// file merely grew) must stay idempotent too.
	if err := db.InsertCompactions(resumed, []ingest.CompactionEvent{shared, newEvent}); err != nil {
		t.Fatal(err)
	}
	occ2, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	if occ2.CompactionCount != 2 {
		t.Errorf("CompactionCount after re-ingest = %d, want 2 (re-inserting must not duplicate)", occ2.CompactionCount)
	}
}

// TestOccupancy_BucketRollup checks tool-name classification and that the
// bucket totals are the sum of their member tools, not a separate count.
func TestOccupancy_BucketRollup(t *testing.T) {
	db := openTestDB(t)
	id := insertTestRun(t, db, "run-a.jsonl")
	if err := db.ReplaceToolUsage(id, map[string]ingest.ToolUsageStat{
		"Read":                               {Calls: 2, ResultBytes: 200},
		"Edit":                               {Calls: 1, ResultBytes: 50},
		"Bash":                               {Calls: 3, ResultBytes: 30},
		"mcp__claude_ai_Drive__search_files": {Calls: 1, ResultBytes: 400},
		"SomeUnclassifiedTool":               {Calls: 1, ResultBytes: 10},
	}); err != nil {
		t.Fatal(err)
	}

	occ, err := db.Occupancy()
	if err != nil {
		t.Fatal(err)
	}
	byBucket := map[string]ToolOutputRow{}
	for _, b := range occ.ByBucket {
		byBucket[b.ToolName] = b
	}
	if r := byBucket["file I/O"]; r.Calls != 3 || r.ResultBytes != 250 {
		t.Errorf("file I/O = %+v, want {Calls:3 ResultBytes:250} (Read+Edit)", r)
	}
	if r := byBucket["shell"]; r.Calls != 3 || r.ResultBytes != 30 {
		t.Errorf("shell = %+v, want {Calls:3 ResultBytes:30}", r)
	}
	if r := byBucket["MCP server"]; r.Calls != 1 || r.ResultBytes != 400 {
		t.Errorf("MCP server = %+v, want {Calls:1 ResultBytes:400}", r)
	}
	if r := byBucket["other"]; r.Calls != 1 || r.ResultBytes != 10 {
		t.Errorf("other = %+v, want {Calls:1 ResultBytes:10} (an unclassified tool must not vanish)", r)
	}
	// Descending by bytes: MCP server (400) before file I/O (250) before
	// shell (30) before other (10).
	if len(occ.ByBucket) < 2 || occ.ByBucket[0].ResultBytes < occ.ByBucket[1].ResultBytes {
		t.Errorf("ByBucket not sorted descending by ResultBytes: %+v", occ.ByBucket)
	}
}

// TestOccupancy_LaneFilter matches ReportForLane's contract: narrowing to a
// lane that has no runs is empty, not an error, and never silently means
// "everything" the way an empty string would.
func TestOccupancy_LaneFilter(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertRun(RunRecord{Path: "a.jsonl", Kind: "session", Lane: "lane-a"}); err != nil {
		t.Fatal(err)
	}
	idA, err := db.RunIDByPath("a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{Path: "b.jsonl", Kind: "session", Lane: "lane-b"}); err != nil {
		t.Fatal(err)
	}
	idB, err := db.RunIDByPath("b.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceToolUsage(idA, map[string]ingest.ToolUsageStat{"Read": {Calls: 1, ResultBytes: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceToolUsage(idB, map[string]ingest.ToolUsageStat{"Bash": {Calls: 1, ResultBytes: 200}}); err != nil {
		t.Fatal(err)
	}

	occ, err := db.OccupancyForLane("lane-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(occ.ByTool) != 1 || occ.ByTool[0].ToolName != "Read" {
		t.Errorf("OccupancyForLane(lane-a).ByTool = %+v, want only Read", occ.ByTool)
	}

	empty, err := db.OccupancyForLane("no-such-lane")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.ByTool) != 0 {
		t.Errorf("OccupancyForLane(no-such-lane).ByTool = %+v, want empty", empty.ByTool)
	}
}
