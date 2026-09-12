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
// assistant lines — a locally-injected status/error message (e.g. a
// rate-limit notice), not a real API call. Confirmed by direct inspection: it
// always carries all-zero usage. It must never be allowed to win "last
// non-empty model" attribution for a run, or a run's real cost (from its
// actual model calls earlier in the same file) gets mislabeled under a model
// name that did no real work and cost nothing — found by running against
// real history, where a failed agent's entire cost was attributed to
// "<synthetic>" because that was the last assistant line before it errored
// out. See docs/transcript-schema.md.
const syntheticModel = "<synthetic>"

// RunSummary is one file's worth of ingest — one main session transcript, or
// one subagent's own transcript. "One file = one run" for B1; the design
// doc's richer run/session distinction (a session containing many runs) is a
// later-phase refinement.
type RunSummary struct {
	Path          string
	SessionID     string
	Model         string // last non-empty model seen; a run can span models
	Kind          string // "session" | "agent"
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
	// nothing rules out spawnDepth > 1 doing so — see docs/design.md's
	// spawnDepth field on the .meta.json companion).
	AgentReconciliations map[string]AgentUsage

	// countedMessages tracks which message.id values have already had their
	// usage added, so one API response written across several transcript
	// lines is counted once. Not part of the summary's output, just
	// bookkeeping for the duration of one file's ingest.
	countedMessages map[string]struct{}
}

// maxLineSize allows for very large lines — a "thinking" block's signature
// field alone was observed to be tens of kilobytes; a generous ceiling here
// is cheaper than a mysterious bufio.Scanner "token too long" failure on a
// real transcript.
const maxLineSize = 16 * 1024 * 1024

// IngestFile reads one JSONL transcript file end to end and produces a
// RunSummary. It never fails on an individual malformed line — ParseLine's
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
		countedMessages:      map[string]struct{}{},
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
	// ingested so far intact — report it, don't discard the partial result.
	if err := sc.Err(); err != nil && err != io.EOF {
		return rs, err
	}

	rs.WeightedCost = WeightedCost(rs.Usage)
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
