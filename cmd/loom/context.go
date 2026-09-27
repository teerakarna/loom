package main

import (
	"fmt"

	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
)

// runContext reports on context occupancy: what filled the window, and what
// compaction cost (docs/design.md, B7b). Deliberately read-only, same reason
// as `loom status` - this answers "what does the ledger already know",
// never "go find out"; run `loom report` first to ingest.
func runContext(args []string) error {
	// issue #94: shared parseArgs (cmd/loom/args.go), replacing this
	// command's own loop, which silently ignored anything that wasn't
	// "--lane" instead of rejecting it - a typo'd flag ran the command as
	// if it had been given no lane at all, with no error.
	_, _, flags, err := parseArgs(args, "usage: loom context [--lane <lane>]", false,
		map[string]string{"--lane": "see `loom status` for the lanes in your ledger"}, "--lane")
	if err != nil {
		return err
	}
	lane := flags["--lane"]

	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	r, err := db.Occupancy()
	if lane != "" {
		r, err = db.OccupancyForLane(lane)
	}
	if err != nil {
		return err
	}
	printOccupancy(r, lane)
	return nil
}

func printOccupancy(r ledger.OccupancyReport, lane string) {
	if lane != "" {
		fmt.Printf("Lane: %s\n\n", ingest.LaneDisplay(lane))
	}

	if len(r.ByTool) == 0 && r.CompactionCount == 0 {
		fmt.Println("Nothing recorded yet. Run `loom report` to ingest.")
		return
	}

	if len(r.ByTool) == 0 {
		fmt.Println("Tool output: none recorded.")
	} else {
		fmt.Println("Tool output, by bucket:")
		for _, b := range r.ByBucket {
			fmt.Printf("  %-14s %8d calls  %12s\n", b.ToolName, b.Calls, formatBytes(b.ResultBytes))
		}

		fmt.Println()
		fmt.Println("Tool output, by tool (top 10):")
		n := len(r.ByTool)
		if n > 10 {
			n = 10
		}
		for _, t := range r.ByTool[:n] {
			fmt.Printf("  %-30s %8d calls  %12s\n", t.ToolName, t.Calls, formatBytes(t.ResultBytes))
		}
	}

	fmt.Println()
	if r.CompactionCount == 0 {
		fmt.Println("Compaction: none recorded.")
		return
	}
	fmt.Printf("Compaction: %d event(s), %s dropped, %s wall clock\n",
		r.CompactionCount, formatTokens(r.CompactionDroppedTokens), formatDuration(r.CompactionWallClockMs))
	fmt.Println("  (dropped tokens are as reported per event, pre minus post - see")
	fmt.Println("   docs/design.md, B7b, for why the host's own cumulative figure is not summed)")
}

// formatBytes renders a byte count for display - a measured figure, never a
// token estimate (docs/design.md, B7b: "bytes are not tokens" stays true at
// the display edge too, so this never silently becomes a token count).
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatTokens is a plain grouped-free integer - no unit conversion, unlike
// formatBytes, because a token count really is a token count here (reported
// directly by the host's own compactMetadata, not derived from bytes).
func formatTokens(n int64) string {
	return fmt.Sprintf("%d tokens", n)
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	s := float64(ms) / 1000
	if s < 60 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%.1fmin", s/60)
}
