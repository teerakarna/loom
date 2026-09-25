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
	// `loom report --lane <lane>` narrows to one project directory; a bare
	// argument is still the projects root, as before.
	var lane string
	for i := 0; i < len(args); i++ {
		if args[i] == "--lane" {
			if i+1 >= len(args) {
				return fmt.Errorf("--lane needs a value (see `loom report` for the lanes in your ledger)")
			}
			lane = args[i+1]
			i++
			continue
		}
		root = args[i]
	}
	// Absolute, always. The walked path is what gets stored as runs.path, so a
	// relative root writes relative rows, and every later comparison against
	// them is textual: `loom report .claude/projects` then `loom status
	// ~/.claude/projects` reported the same ingested, unchanged file as both
	// "never ingested" and "in ledger, outside root". Normalising at the two
	// entry points is what makes the ledger's paths comparable at all.
	root, err = filepath.Abs(root)
	if err != nil {
		return err
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
	if lane != "" {
		summary, err = db.ReportForLane(lane)
	}
	if err != nil {
		return err
	}
	if lane != "" {
		fmt.Printf("Lane:            %s\n", ingest.LaneDisplay(lane))
		if summary.TotalRuns == 0 {
			fmt.Println("No runs in that lane. `loom report` with no filter lists the lanes it knows.")
			return nil
		}
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
	// Before the walk, deliberately. The prune reads the ledger and touches no
	// file, so making it wait behind a walk that can fail means the one remedy
	// `loom status` names for a prunable row does not run in the states where
	// the walk errors - including a projects root that does not exist, where
	// status prints "`loom report` deletes these rows" and report exits on an
	// lstat before reaching this line.
	if err := pruneWorkflowJournals(db); err != nil {
		return err
	}

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

	// Fetched once for the whole batch, not once per run: the assets
	// table does not change mid-batch, and it is small (design doc
	// "Ledger"). See ledger.BuildAssetLookup.
	lookup, err := db.BuildAssetLookup()
	if err != nil {
		return err
	}

	for path, rs := range summaries {
		rec := ledger.RunRecord{
			Path:                path,
			SizeBytes:           sizes[path],
			SessionID:           rs.SessionID,
			Kind:                rs.Kind,
			Model:               rs.Model,
			Lane:                ingest.LaneFromPath(root, path),
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
			continue
		}
		if err := recordOccupancyAndUsage(db, path, lookup, rs); err != nil {
			fmt.Fprintf(os.Stderr, "loom: failed to record occupancy/usage for %s: %v\n", path, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// recordOccupancyAndUsage writes rs's tool_usage, compaction, and
// asset_usage rows (B7b, B7a/#39). One RunIDByPath lookup shared across
// all three, rather than a separate lookup per table for a row InsertRun
// just wrote a moment earlier in this same loop iteration - found by code
// review as a redundant-query smell, not a correctness bug, but free to fix
// in the same pass.
func recordOccupancyAndUsage(db *ledger.DB, path string, lookup ledger.AssetLookup, rs ingest.RunSummary) error {
	runID, err := db.RunIDByPath(path)
	if err != nil {
		return err
	}
	if err := db.ReplaceToolUsage(runID, rs.ToolUsage); err != nil {
		return err
	}
	if err := db.InsertCompactions(runID, rs.Compactions); err != nil {
		return err
	}
	// A skill invoked under a name discovery has not seen, or a file outside
	// any known asset's path, resolves to nothing - this answers "was a
	// known asset used", not "what files exist". Requires discovery to
	// have found something at least once (an empty lookup resolves every
	// signal to nothing), the same precondition #38's staleness check
	// already has.
	touches := lookup.Resolve(rs.SkillTouches, rs.FileTouches)
	return db.ReplaceAssetUsage(runID, touches)
}

func printReport(s ledger.Summary) {
	fmt.Printf("Runs ingested:   %d (%d sessions, %d agents)\n", s.TotalRuns, s.SessionRuns, s.AgentRuns)
	fmt.Printf("Weighted cost:   %.0f (relative units - see docs/design.md, not real currency)\n", s.TotalWeightedCost)
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

	if len(s.ByLane) > 1 {
		printGroup(s, "By lane:", len(s.ByLane), func(i int) (string, int, float64, float64) {
			l := s.ByLane[i]
			return ingest.LaneDisplay(l.Lane), l.Runs, l.WeightedCost, l.PerRun
		})
	}

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
		fmt.Println("task-notification. This is NOT reconciled against the weighted cost above -")
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

// pruneWorkflowJournals drops rows the ingester should never have written: a
// ledger built before ingest.IsTranscript existed holds one run per workflow
// orchestration journal, with no model, no tokens and no tool calls. Nothing
// else removes them - reports, status and the MCP cost summary all count
// `runs` unfiltered, so they keep inflating the totals for as long as the
// ledger lives.
//
// Deliberately keyed on the one known-bad filename rather than on
// !IsTranscript, even though that reads as the more general fix. The
// complement of a whitelist has unbounded blast radius: if the host ever
// changes the subagent naming convention, or a ledger carries rows from a host
// that used a different one, every subagent run under subagents/ would be
// deleted with its child rows, and Walk would skip the same files so nothing
// would re-ingest them. A phantom row that survives is a wrong number; a
// wrongly deleted row is lost data. The paths go to stderr for the same
// reason - a count alone leaves no way to see what went.
//
// Keyed on the path shape rather than on cost, too: a genuinely zero-cost run
// (a subagent that produced no assistant turn) is real data and must survive.
func pruneWorkflowJournals(db *ledger.DB) error {
	known, err := db.KnownRuns()
	if err != nil {
		return err
	}
	var stale []string
	for _, k := range known {
		if isPrunableLedgerPath(k.Path) {
			stale = append(stale, k.Path)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	deleted, err := db.DeleteRuns(stale)
	if err != nil {
		return err
	}
	// Report what was deleted, not what was a candidate. DeleteRuns skips a path
	// with no row rather than failing, so the two lists can differ, and printing
	// the candidates claims prunes that did not happen - in output whose whole
	// purpose is making a drop in the run count explainable.
	//
	// The reachable cause is a second `loom report` on the same ledger, and it
	// involves no lock contention at all: KnownRuns above runs outside any
	// transaction, DeleteRuns opens its own, and a concurrent prune that commits
	// in that window leaves our SELECT with no row to find. (An earlier version
	// of this comment claimed the opposite - that such a race would surface as
	// SQLITE_BUSY - and also offered a duplicate candidate path as the cause,
	// which runs.path being UNIQUE makes impossible. Both wrong.)
	for _, p := range deleted {
		fmt.Fprintf(os.Stderr, "loom: pruned non-transcript ledger row %s\n", p)
	}
	return nil
}

// isPrunableLedgerPath reports whether a ledger row's path is one `loom report`
// will delete. Shared with `loom status`, which tells the user that running a
// report clears these rows: keying both on one predicate is what makes that
// claim true rather than merely true today. The two drifting apart is not a
// hypothetical - status's first version inferred "not a transcript" from "Walk
// did not return it", which counted every row outside a narrowed root as
// prunable and pointed them at a remedy that would never touch them.
func isPrunableLedgerPath(path string) bool {
	return filepath.Base(path) == "journal.jsonl" && !ingest.IsTranscript(path)
}
