package ingest

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// syntheticModel is a real, confirmed value of message.model on some
// assistant lines - a locally-injected status/error message (e.g. a
// rate-limit notice), not a real API call. Confirmed by direct inspection: it
// always carries all-zero usage. It must never be allowed to win "last
// non-empty model" attribution for a run, or a run's real cost (from its
// actual model calls earlier in the same file) gets mislabeled under a model
// name that did no real work and cost nothing - found by running against
// real history, where a failed agent's entire cost was attributed to
// "<synthetic>" because that was the last assistant line before it errored
// out. See docs/transcript-schema.md.
const syntheticModel = "<synthetic>"

// unknownTool is the ToolUsage bucket for a tool_result whose matching
// tool_use block was not seen in this file - the call happened in a
// transcript this file doesn't contain (e.g. before a resume). Attributed
// here rather than dropped, so the byte count is never silently lost.
const unknownTool = "(unknown)"

// ToolUsageStat aggregates one tool's calls and result size within a run.
// ResultBytes is a measured byte count, never a token estimate - see
// docs/design.md, B7b, "bytes are not tokens".
type ToolUsageStat struct {
	Calls       int
	ResultBytes int64
}

// RunSummary is one file's worth of ingest - one main session transcript, or
// one subagent's own transcript. "One file = one run" for B1; the design
// doc's richer run/session distinction (a session containing many runs) is a
// later-phase refinement.
type RunSummary struct {
	Path      string
	SessionID string
	Model     string // last non-empty model seen; a run can span models
	Kind      string // "session" | "agent"
	// AgentType is the subagent's declared type ("Explore", "fork", ...),
	// read from the .meta.json companion. Empty for session runs, and for
	// agent runs whose companion is missing or unreadable. This is what B3's
	// per-agent-type policy keys on.
	AgentType string
	// Effort is the reasoning effort observed on this run's assistant lines.
	// Last non-empty value wins, same rule as Model: a run can in principle
	// span efforts, and the most recent is the better description of it.
	// Empty when no line carried one.
	Effort        string
	Usage         Usage
	WeightedCost  float64
	ToolUseCount  int
	DenialCount   int
	FeedbackCount int
	StartedAt     time.Time
	EndedAt       time.Time

	// Reconciliation data: <usage> blocks found in this file's own
	// task-notification lines, keyed by the agent's task-id. Only main
	// session files produce these (a subagent's own transcript doesn't
	// launch further subagents in the cases inspected so far, though
	// nothing rules out spawnDepth > 1 doing so - see docs/design.md's
	// spawnDepth field on the .meta.json companion).
	AgentReconciliations map[string]AgentUsage

	// ToolUsage aggregates tool_result byte counts by the tool that produced
	// them (B7b). Keyed by tool name, resolved via the matching tool_use's
	// id - see docs/transcript-schema.md, "tool_use / tool_result".
	ToolUsage map[string]ToolUsageStat

	// Compactions is every compact_boundary this file recorded, in file
	// order. Cross-file dedup - a resumed session replays its prior
	// compaction history verbatim - happens at the ledger layer, keyed on
	// each event's UUID; see docs/transcript-schema.md.
	Compactions []CompactionEvent

	// countedMessages tracks which message.id values have already had their
	// usage added, so one API response written across several transcript
	// lines is counted once. Not part of the summary's output, just
	// bookkeeping for the duration of one file's ingest.
	countedMessages map[string]struct{}
	// toolNames maps a tool_use id to its tool name, for this file only -
	// bookkeeping for resolving ToolResults, not part of the output.
	toolNames map[string]string
}

// maxLineSize allows for very large lines - a "thinking" block's signature
// field alone was observed to be tens of kilobytes; a generous ceiling here
// is cheaper than a mysterious bufio.Scanner "token too long" failure on a
// real transcript.
const maxLineSize = 16 * 1024 * 1024

// IngestFile reads one JSONL transcript file end to end and produces a
// RunSummary. It never fails on an individual malformed line - ParseLine's
// schema-tolerance means a bad line is just skipped, not fatal to the file.
func IngestFile(path string) (RunSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return RunSummary{}, err
	}
	defer func() { _ = f.Close() }()

	rs := RunSummary{
		Path:                 path,
		Kind:                 kindForPath(path),
		AgentReconciliations: map[string]AgentUsage{},
		ToolUsage:            map[string]ToolUsageStat{},
		countedMessages:      map[string]struct{}{},
		toolNames:            map[string]string{},
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineSize)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		ev, ok := ParseLine(line)
		if !ok {
			continue
		}
		applyEvent(&rs, ev)
	}
	// A line-too-long or read error partway through still leaves everything
	// ingested so far intact - report it, don't discard the partial result.
	if err := sc.Err(); err != nil && err != io.EOF {
		return rs, err
	}

	rs.WeightedCost = WeightedCost(rs.Usage)

	// Agent type lives only in the .meta.json companion, never in the
	// transcript itself. A missing or unreadable companion leaves AgentType
	// empty rather than failing the ingest: the run's cost is still real and
	// worth recording, it just cannot be attributed to an agent type.
	if rs.Kind == "agent" {
		if meta, ok := ReadAgentMeta(path); ok {
			rs.AgentType = meta.AgentType
		}
	}
	return rs, nil
}

// alreadyCounted reports whether id's usage has been added already, recording
// it if not. An empty id (a line with no message.id) is never treated as a
// duplicate: there is nothing to key on, and skipping it would undercount.
func (rs *RunSummary) alreadyCounted(id string) bool {
	if id == "" {
		return false
	}
	if _, seen := rs.countedMessages[id]; seen {
		return true
	}
	rs.countedMessages[id] = struct{}{}
	return false
}

func applyEvent(rs *RunSummary, ev Event) {
	if rs.SessionID == "" {
		rs.SessionID = ev.SessionID
	}
	if !ev.Timestamp.IsZero() {
		if rs.StartedAt.IsZero() || ev.Timestamp.Before(rs.StartedAt) {
			rs.StartedAt = ev.Timestamp
		}
		if ev.Timestamp.After(rs.EndedAt) {
			rs.EndedAt = ev.Timestamp
		}
	}
	if ev.Model != "" && ev.Model != syntheticModel {
		rs.Model = ev.Model
	}
	if ev.Effort != "" {
		rs.Effort = ev.Effort
	}
	// Usage is counted once per API response, not once per transcript line.
	// One response is written as several lines (one per content block:
	// thinking, text, each tool_use) and every one of them repeats the same
	// usage object, so summing per line overstated real cost by ~2.1x on a
	// real corpus - see docs/transcript-schema.md and the regression test in
	// run_test.go. A line with no message.id at all still counts, since
	// there's nothing to deduplicate it against and dropping it would
	// undercount instead.
	if ev.Usage != nil && !rs.alreadyCounted(ev.MessageID) {
		rs.Usage = rs.Usage.Add(*ev.Usage)
	}

	// ToolUseCount is deliberately NOT deduplicated: each line carries its
	// own distinct content blocks, so two tool_use blocks arriving on two
	// lines of the same response really are two separate tool calls.
	rs.ToolUseCount += ev.ToolUseCount
	if ev.ToolDenialKind != "" {
		rs.DenialCount++
	}
	if ev.UserFeedback != "" {
		rs.FeedbackCount++
	}
	if tn := ev.TaskNotification; tn != nil && tn.Usage != nil {
		rs.AgentReconciliations[tn.TaskID] = *tn.Usage
	}

	// tool_use blocks arrive on assistant lines, always before the
	// tool_result that refers back to them (confirmed on a real corpus -
	// see docs/transcript-schema.md), so recording id->name here and
	// resolving ToolResults below in the same forward pass is enough.
	for _, tu := range ev.ToolUses {
		rs.toolNames[tu.ID] = tu.Name
	}
	for _, tr := range ev.ToolResults {
		name := rs.toolNames[tr.ToolUseID]
		if name == "" {
			name = unknownTool
		}
		stat := rs.ToolUsage[name]
		stat.Calls++
		stat.ResultBytes += tr.Bytes
		rs.ToolUsage[name] = stat
	}

	if ev.Compaction != nil {
		rs.Compactions = append(rs.Compactions, *ev.Compaction)
	}
}

func kindForPath(path string) string {
	if strings.Contains(filepath.ToSlash(path), "/subagents/") {
		return "agent"
	}
	return "session"
}

// Walk discovers transcript files under a Claude Code projects root
// (typically ~/.claude/projects): every top-level session .jsonl file, plus
// every subagent's own agent-*.jsonl file under <session-id>/subagents/.
// Files are returned in no particular order; the caller decides ingest order.
func Walk(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}
