package ingest

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	defer f.Close()

	rs := RunSummary{
		Path:                 path,
		Kind:                 kindForPath(path),
		AgentReconciliations: map[string]AgentUsage{},
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
	if ev.Model != "" {
		rs.Model = ev.Model
	}
	if ev.Usage != nil {
		rs.Usage = rs.Usage.Add(*ev.Usage)
	}
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
