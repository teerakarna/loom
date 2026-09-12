package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
)

// defaultProjectsRoot is where Claude Code writes session transcripts.
func defaultProjectsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// defaultLedgerPath is where Loom's own SQLite ledger lives.
func defaultLedgerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".loom")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "loom.db"), nil
}

func runReport(args []string) error {
	root, err := defaultProjectsRoot()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		root = args[0]
	}

	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := ingestAll(db, root); err != nil {
		return err
	}

	summary, err := db.Report()
	if err != nil {
		return err
	}
	printReport(summary)
	return nil
}

// ingestAll walks root, ingests every transcript file not already in the
// ledger, and inserts one run per file. Reconciliation matching (attaching a
// session's reported <usage> figures to the corresponding agent run) happens
// after every file in the batch has been read, since a session file can
// reference an agent whose own file is discovered in any order during the
// walk.
func ingestAll(db *ledger.DB, root string) error {
	paths, err := ingest.Walk(root)
	if err != nil {
		return err
	}

	var toInsert []string
	for _, p := range paths {
		has, err := db.HasRun(p)
		if err != nil {
			return err
		}
		if !has {
			toInsert = append(toInsert, p)
		}
	}
	if len(toInsert) == 0 {
		return nil
	}

	summaries := make(map[string]ingest.RunSummary, len(toInsert))
	for _, p := range toInsert {
		rs, err := ingest.IngestFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom: skipping %s: %v\n", p, err)
			continue
		}
		summaries[p] = rs
	}

	// Reconciliation figures reported by any session in this batch, keyed by
	// agent id, regardless of which session file reported them.
	reported := map[string]ingest.AgentUsage{}
	for _, rs := range summaries {
		for taskID, u := range rs.AgentReconciliations {
			reported[taskID] = u
		}
	}

	for path, rs := range summaries {
		rec := ledger.RunRecord{
			Path:                path,
			SessionID:           rs.SessionID,
			Kind:                rs.Kind,
			Model:               rs.Model,
			AgentType:           rs.AgentType,
			Effort:              rs.Effort,
			StartedAt:           rs.StartedAt,
			EndedAt:             rs.EndedAt,
			InputTokens:         rs.Usage.InputTokens,
			OutputTokens:        rs.Usage.OutputTokens,
			CacheReadTokens:     rs.Usage.CacheReadInputTokens,
			CacheCreationTokens: rs.Usage.CacheCreationInputTokens,
			WeightedCost:        rs.WeightedCost,
			ToolUseCount:        rs.ToolUseCount,
			DenialCount:         rs.DenialCount,
			FeedbackCount:       rs.FeedbackCount,
		}
		if rs.Kind == "agent" {
			if id, ok := ingest.AgentIDFromPath(path); ok {
				if u, ok := reported[id]; ok {
					rec.ReportedSubagentTokens = ptr(u.SubagentTokens)
					rec.ReportedToolUses = ptr(u.ToolUses)
					rec.ReportedDurationMs = ptr(u.DurationMs)
				}
			}
		}
		if err := db.InsertRun(rec); err != nil {
			fmt.Fprintf(os.Stderr, "loom: failed to record %s: %v\n", path, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func printReport(s ledger.Summary) {
	fmt.Printf("Runs ingested:   %d (%d sessions, %d agents)\n", s.TotalRuns, s.SessionRuns, s.AgentRuns)
	fmt.Printf("Weighted cost:   %.0f (relative units — see docs/design.md, not real currency)\n", s.TotalWeightedCost)
	fmt.Printf("Tool uses:       %d\n", s.TotalToolUses)
	fmt.Printf("Tool denials:    %d\n", s.TotalDenials)
	fmt.Printf("User feedback:   %d\n", s.TotalFeedback)
	fmt.Println()
	fmt.Println("By model:")
	for _, mc := range s.ByModel {
		fmt.Printf("  %-24s %6d runs  %12.0f\n", mc.Model, mc.Runs, mc.WeightedCost)
	}
	if s.AgentRuns > 0 {
		fmt.Println()
		fmt.Printf("%d/%d agent runs have a reported subagent_tokens figure from their parent's\n", s.UnreconciledAgents, s.AgentRuns)
		fmt.Println("task-notification. This is NOT reconciled against the weighted cost above —")
		fmt.Println("see docs/transcript-schema.md, \"Reconciliation does NOT hold\".")
	}
}
