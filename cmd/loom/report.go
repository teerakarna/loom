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

	// Size is what decides: a transcript that has grown since it was last
	// ingested must be re-read, or a live session's cost stays frozen at
	// whatever it was the first time loom looked (see ledger.NeedsIngest).
	sizes := map[string]int64{}
	var toInsert []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom: skipping %s: %v\n", p, err)
			continue
		}
		sizes[p] = fi.Size()
		needs, err := db.NeedsIngest(p, fi.Size())
		if err != nil {
			return err
		}
		if needs {
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
			SizeBytes:           sizes[path],
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
	printCostByKind(s)
	fmt.Printf("Tool uses:       %d\n", s.TotalToolUses)
	fmt.Printf("Tool denials:    %d\n", s.TotalDenials)
	fmt.Printf("User feedback:   %d\n", s.TotalFeedback)

	// Concentration first: it changes how every breakdown below should be
	// read. If a handful of runs are nearly all the cost, the averages are
	// describing the tail, not the typical case.
	if len(s.Concentration) > 0 && s.TotalRuns > 1 {
		fmt.Println()
		fmt.Println("Cost concentration:")
		for _, c := range s.Concentration {
			if c.N >= s.TotalRuns {
				break
			}
			fmt.Printf("  top %-2d of %d runs   %5.1f%% of total\n", c.N, s.TotalRuns, c.Share*100)
		}
	}

	printGroup(s, "By model:", len(s.ByModel), func(i int) (string, int, float64, float64) {
		m := s.ByModel[i]
		return m.Model, m.Runs, m.WeightedCost, m.PerRun
	})

	if len(s.ByAgentType) > 0 {
		printGroup(s, "By agent type:", len(s.ByAgentType), func(i int) (string, int, float64, float64) {
			a := s.ByAgentType[i]
			name := a.AgentType
			if name == "" {
				name = "(unattributed)"
			}
			return name, a.Runs, a.WeightedCost, a.PerRun
		})
	}

	if len(s.TopRuns) > 0 && s.TotalRuns > 1 {
		fmt.Println()
		fmt.Println("Most expensive runs:")
		for _, r := range s.TopRuns {
			label := r.Model
			if r.AgentType != "" {
				label = r.AgentType + " / " + r.Model
			}
			fmt.Printf("  %5.1f%%  %-30s %12.0f  %s\n", r.Share*100, label, r.WeightedCost, filepath.Base(r.Path))
		}
	}

	if s.AgentRuns > 0 {
		fmt.Println()
		fmt.Printf("%d/%d agent runs have a reported subagent_tokens figure from their parent's\n", s.UnreconciledAgents, s.AgentRuns)
		fmt.Println("task-notification. This is NOT reconciled against the weighted cost above —")
		fmt.Println("see docs/transcript-schema.md, \"Reconciliation does NOT hold\".")
	}
}

// printCostByKind shows what the headline figure is actually made of. A bare
// nine-digit total has no reference point, and hides the most useful fact
// about a corpus: spend dominated by cheap cached input and spend dominated by
// expensive fresh output look identical in the total and imply opposite
// actions (issue #9).
func printCostByKind(s ledger.Summary) {
	if len(s.CostByKind) == 0 || s.TotalWeightedCost == 0 {
		return
	}
	fmt.Println("  made up of:")
	for _, k := range s.CostByKind {
		if k.Tokens == 0 {
			continue
		}
		fmt.Printf("    %-12s %5.1f%%  %15d tokens x %.2f\n", k.Kind, k.Share*100, k.Tokens, weightFor(k.Kind))
	}
}

// weightFor is display-only, reading the same exported constants the cost
// function uses so the printed multiplier cannot drift from the real one.
func weightFor(kind string) float64 {
	switch kind {
	case "input":
		return ingest.WeightInput
	case "output":
		return ingest.WeightOutput
	case "cache read":
		return ingest.WeightCacheRead
	default:
		return ingest.WeightCacheWrite
	}
}

// printGroup renders one grouped breakdown with both the total and the
// per-run figure. Per-run is shown because totals alone conflate "costs more
// each time" with "used more often", and can invert the real ordering when
// run counts differ (issue #10).
func printGroup(s ledger.Summary, heading string, n int, row func(int) (string, int, float64, float64)) {
	if n == 0 {
		return
	}
	fmt.Println()
	fmt.Println(heading)
	fmt.Printf("  %-24s %6s %14s %14s\n", "", "runs", "total", "per run")
	for i := range n {
		name, runs, total, per := row(i)
		fmt.Printf("  %-24s %6d %14.0f %14.0f\n", name, runs, total, per)
	}
}
