package main

import (
	"fmt"
	"os"

	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/policy"
)

// runStatus reports on Loom itself: what it knows, how current it is, and what
// it cannot answer. Deliberately read-only. `loom report` ingests as a side
// effect, so a user checking freshness with it would change the thing they
// were checking; this never writes.
func runStatus(args []string) error {
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

	st, err := db.Status()
	if err != nil {
		return err
	}

	fmt.Printf("Ledger:    %s", ledgerPath)
	if fi, err := os.Stat(ledgerPath); err == nil {
		fmt.Printf("  (%.1f MB)", float64(fi.Size())/(1024*1024))
	}
	fmt.Println()
	fmt.Println("           machine-local by design; never sync or commit it (design constraint 12)")
	fmt.Println()

	fresh, err := freshness(db, root)
	if err != nil {
		return err
	}
	fmt.Println("Freshness:")
	fmt.Printf("  transcripts on disk       %d\n", fresh.onDisk)
	fmt.Printf("  ingested and current      %d\n", fresh.current)
	if fresh.stale > 0 {
		fmt.Printf("  ingested but STALE        %d  (grown since; run `loom report`)\n", fresh.stale)
	} else {
		fmt.Printf("  ingested but stale        0\n")
	}
	if fresh.unseen > 0 {
		fmt.Printf("  never ingested            %d  (run `loom report`)\n", fresh.unseen)
	} else {
		fmt.Printf("  never ingested            0\n")
	}
	if fresh.missing > 0 {
		fmt.Printf("  in ledger, gone from disk %d\n", fresh.missing)
	}
	if fresh.prunable > 0 {
		// Label width matches the line above deliberately: the two counts are
		// read against each other, and a one-character shift makes them look
		// like different columns.
		fmt.Printf("  in ledger, not transcript %d  (file still there; `loom report` prunes workflow journals)\n", fresh.prunable)
	}
	fmt.Println()

	fmt.Println("Ledger contents:")
	fmt.Printf("  runs         %d (%d sessions, %d agents)\n", st.Runs, st.SessionRuns, st.AgentRuns)
	if st.Unattributed > 0 {
		fmt.Printf("               %d agent run(s) have no readable .meta.json, so no agent type\n", st.Unattributed)
	}
	fmt.Printf("  agent types  %d\n", st.AgentTypes)
	fmt.Printf("  lanes        %d\n", st.Lanes)
	if st.UnattributedLanes > 0 {
		fmt.Printf("               %d run(s) sit outside the projects root, so no lane\n", st.UnattributedLanes)
	}
	fmt.Printf("  assets       %d (%d stale)\n", st.Assets, st.StaleAsset)
	fmt.Printf("  events       %d\n", st.Events)
	fmt.Printf("  policies     %d deliberate\n", st.Policies)
	if st.EarliestRun != "" {
		fmt.Printf("  run window   %s to %s\n", st.EarliestRun, st.LatestRun)
	}
	fmt.Println()

	if summary, err := db.Report(); err == nil && len(summary.ByLane) > 1 {
		fmt.Println("Lanes seen:")
		for _, l := range summary.ByLane {
			fmt.Printf("  %-28s %3d run(s)\n", ingest.LaneDisplay(l.Lane), l.Runs)
		}
	}
	fmt.Println()

	stats, err := db.StatsByAgentType()
	if err != nil {
		return err
	}
	ready := 0
	for _, s := range stats {
		if s.Runs >= policy.MinSampleSize {
			ready++
		}
	}
	fmt.Println("Policy readiness:")
	fmt.Printf("  %d of %d agent type(s) have the %d runs needed before evidence displaces a default\n",
		ready, len(stats), policy.MinSampleSize)
	if ready == 0 && len(stats) > 0 {
		fmt.Println("  everything is resolving to a labelled shipped default, which is correct, not a fault")
	}
	fmt.Println()

	// Saying what this cannot answer is part of the job. A status command that
	// implies completeness it does not have is worse than none.
	fmt.Println("Not tracked yet:")
	fmt.Println("  - when each run was ingested (only when the run itself happened)")
	fmt.Println("  - which files failed to parse on the last pass (logged to stderr, then forgotten)")
	return nil
}

type freshnessCounts struct{ onDisk, current, stale, unseen, missing, prunable int }

// freshness compares the ledger against what is on disk right now, without
// ingesting. This is the check that would have surfaced the staleness bug:
// a transcript that has grown since it was read is reported here rather than
// silently under-counted in the next report.
func freshness(db *ledger.DB, root string) (freshnessCounts, error) {
	var f freshnessCounts

	known := map[string]int64{}
	rows, err := db.KnownRuns()
	if err != nil {
		return f, err
	}
	for _, r := range rows {
		known[r.Path] = r.SizeBytes
	}

	paths, err := ingest.Walk(root)
	if err != nil {
		// A missing projects root is a normal state for a new install, not an
		// error worth failing the whole command over.
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}

	onDisk := map[string]bool{}
	for _, p := range paths {
		onDisk[p] = true
		f.onDisk++
		size, ok := known[p]
		if !ok {
			f.unseen++
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.Size() != size {
			f.stale++
		} else {
			f.current++
		}
	}
	for p := range known {
		if onDisk[p] {
			continue
		}
		// Walk did not return it, which is two different states, and
		// conflating them misreports both. The file may be genuinely gone -
		// or it may be sitting right there and simply not be a transcript (a
		// workflow's orchestration journal, ingested before IsTranscript
		// existed), in which case "gone from disk" is a false signal that
		// never clears. Stat decides; status stays read-only either way, and
		// the next `loom report` is what removes the row.
		if _, err := os.Stat(p); err == nil {
			f.prunable++
			continue
		}
		f.missing++
	}
	return f, nil
}
