package ledger

import (
	"database/sql"
	"strings"

	"github.com/teerakarna/loom/internal/ingest"
)

// RunIDByPath returns the id of the runs row for path, so a caller that just
// called InsertRun can attach tool_usage/compactions rows to it. path is
// UNIQUE on runs, so this is always at most one row.
func (d *DB) RunIDByPath(path string) (int64, error) {
	var id int64
	err := d.sql.QueryRow(`SELECT id FROM runs WHERE path = ?`, path).Scan(&id)
	return id, err
}

// ReplaceToolUsage writes runID's tool_usage rows, one per event, replacing
// whatever this run previously owned. The DELETE covers only rows this
// run_id owns, so a re-ingest of a grown file rebuilds cleanly; the
// ON CONFLICT(tool_use_id) DO NOTHING on insert is what stops a resumed
// session's replayed events from being recounted under a second run_id -
// first-seen keeps ownership, the same rule InsertCompactions uses for
// boundary_uuid. Found missing by code review: an earlier version
// aggregated calls/bytes per tool name with no per-event identity to dedupe
// against, and double-counted every tool call a resumed session replayed.
func (d *DB) ReplaceToolUsage(runID int64, events []ingest.ToolUsageEvent) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM tool_usage WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO tool_usage (tool_use_id, run_id, tool_name, result_bytes)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tool_use_id) DO NOTHING`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, ev := range events {
		if _, err := stmt.Exec(ev.ToolUseID, runID, ev.ToolName, ev.ResultBytes); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertCompactions writes runID's compaction events, deduped globally on
// each event's boundary UUID (docs/transcript-schema.md, "compact_boundary"
// - a resumed session replays its prior compaction history verbatim). An
// event whose UUID is already recorded, under this run or any other, is left
// alone: first-seen keeps ownership, which is the only attribution that
// survives a file being re-ingested after it grows.
func (d *DB) InsertCompactions(runID int64, events []ingest.CompactionEvent) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO compactions (
			run_id, boundary_uuid, seq, trigger, pre_tokens, post_tokens,
			cumulative_dropped, duration_ms, at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(boundary_uuid) DO NOTHING`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for i, ev := range events {
		if _, err := stmt.Exec(runID, ev.UUID, i+1, ev.Trigger, ev.PreTokens, ev.PostTokens,
			ev.CumulativeDroppedTokens, ev.DurationMs, formatTime(ev.Timestamp)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToolOutputRow is one tool's aggregate call count and result size, with the
// bucket it rolls up into (docs/design.md, B7b's measured table).
type ToolOutputRow struct {
	ToolName    string
	Bucket      string
	Calls       int
	ResultBytes int64
}

// CompactionRow is one compaction event, for `loom context`'s per-session
// detail. DroppedTokens is PreTokens - PostTokens, the per-event figure -
// not CumulativeDropped, which is cumulative within a session lineage and
// not meaningful on its own per event.
type CompactionRow struct {
	RunPath       string
	Seq           int
	Trigger       string
	PreTokens     int64
	PostTokens    int64
	DroppedTokens int64
	DurationMs    int64
	At            string
}

// OccupancyReport is what `loom context` and the MCP get_context_occupancy
// tool both read. Bytes throughout, never tokens - see docs/design.md, B7b,
// "bytes are not tokens".
type OccupancyReport struct {
	ByTool   []ToolOutputRow // descending by ResultBytes
	ByBucket []ToolOutputRow // ToolName is the bucket name here; descending by ResultBytes

	CompactionCount         int
	CompactionDroppedTokens int64 // sum of per-event PreTokens-PostTokens, deduped
	CompactionWallClockMs   int64
	Compactions             []CompactionRow // chronological
}

// bucketFor classifies a tool name the same way docs/design.md's measured
// table does. Anything not recognised falls into "other" rather than being
// hidden - an unclassified tool should show up as a gap to fill, not vanish.
func bucketFor(name string) string {
	switch {
	case name == "Read" || name == "Edit" || name == "Write" || name == "NotebookEdit":
		return "file I/O"
	case name == "Bash":
		return "shell"
	case strings.HasPrefix(name, "mcp__"):
		return "MCP server"
	case name == "WebSearch" || name == "WebFetch":
		return "web"
	case name == "ExitPlanMode" || name == "EnterPlanMode" || name == "AskUserQuestion" || name == "ToolSearch":
		return "harness / UI"
	case name == "Agent" || name == "TaskCreate" || name == "TaskUpdate" || name == "TaskList" ||
		name == "TaskGet" || name == "TaskStop" || name == "ListAgents" || name == "SendMessage" ||
		name == "Monitor" || name == "ScheduleWakeup":
		return "delegation"
	case name == ingest.UnknownTool:
		return ingest.UnknownTool
	default:
		return "other"
	}
}

// Occupancy computes the whole-ledger occupancy report. ReportOccupancyForLane
// narrows the same report to one lane, same split as Report/ReportForLane.
func (d *DB) Occupancy() (OccupancyReport, error) { return d.occupancy("") }

// OccupancyForLane is Occupancy restricted to runs from one lane.
func (d *DB) OccupancyForLane(lane string) (OccupancyReport, error) { return d.occupancy(lane) }

func (d *DB) occupancy(lane string) (OccupancyReport, error) {
	var r OccupancyReport

	where, args := "", []any(nil)
	if lane != "" {
		where, args = " WHERE r.lane = ?", []any{lane}
	}

	rows, err := d.sql.Query(`
		SELECT tu.tool_name, COUNT(*), COALESCE(SUM(tu.result_bytes),0)
		FROM tool_usage tu JOIN runs r ON r.id = tu.run_id`+where+`
		GROUP BY tu.tool_name ORDER BY SUM(tu.result_bytes) DESC`, args...)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	bucketIndex := map[string]int{}
	for rows.Next() {
		var t ToolOutputRow
		if err := rows.Scan(&t.ToolName, &t.Calls, &t.ResultBytes); err != nil {
			return r, err
		}
		t.Bucket = bucketFor(t.ToolName)
		r.ByTool = append(r.ByTool, t)

		i, ok := bucketIndex[t.Bucket]
		if !ok {
			i = len(r.ByBucket)
			bucketIndex[t.Bucket] = i
			r.ByBucket = append(r.ByBucket, ToolOutputRow{ToolName: t.Bucket, Bucket: t.Bucket})
		}
		r.ByBucket[i].Calls += t.Calls
		r.ByBucket[i].ResultBytes += t.ResultBytes
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	sortToolOutputByBytes(r.ByBucket)

	crows, err := d.sql.Query(`
		SELECT r.path, c.seq, c.trigger, c.pre_tokens, c.post_tokens, c.duration_ms, c.at
		FROM compactions c JOIN runs r ON r.id = c.run_id`+where+`
		ORDER BY c.at ASC`, args...)
	if err != nil {
		return r, err
	}
	defer func() { _ = crows.Close() }()
	for crows.Next() {
		var c CompactionRow
		var at sql.NullString // at is nullable - see ledger.go, "at TEXT"
		if err := crows.Scan(&c.RunPath, &c.Seq, &c.Trigger, &c.PreTokens, &c.PostTokens, &c.DurationMs, &at); err != nil {
			return r, err
		}
		c.At = at.String
		c.DroppedTokens = c.PreTokens - c.PostTokens
		r.Compactions = append(r.Compactions, c)
		r.CompactionCount++
		r.CompactionDroppedTokens += c.DroppedTokens
		r.CompactionWallClockMs += c.DurationMs
	}
	return r, crows.Err()
}

// sortToolOutputByBytes orders rows by ResultBytes descending. Small enough
// (at most a handful of buckets) that a stdlib sort would be overkill
// ceremony; insertion sort is plenty and keeps this file dependency-free.
func sortToolOutputByBytes(rows []ToolOutputRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].ResultBytes > rows[j-1].ResultBytes; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
