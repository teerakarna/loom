package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// Printed whenever non-zero so the four counts above still add up to
	// `transcripts on disk`. A file loom found but could not stat is neither
	// current nor stale nor unseen, and silently dropping it makes the
	// arithmetic wrong with nothing on screen to explain the gap.
	if fresh.unreadableOnDisk > 0 {
		fmt.Printf("  ingested but unreadable   %d  (could not stat to compare size; state unknown)\n", fresh.unreadableOnDisk)
	}
	// Label widths match deliberately: these counts are read against each
	// other, and a one-character shift makes them look like separate columns.
	if fresh.missing > 0 {
		fmt.Printf("  in ledger, gone from disk %d\n", fresh.missing)
	}
	if fresh.unreadable > 0 {
		fmt.Printf("  in ledger, unreadable     %d  (permissions or an unavailable mount, not data loss)\n", fresh.unreadable)
	}
	// No claim about the file here. This bucket is keyed on the path alone, so
	// the row is prunable whether the file is still on disk or not, and saying
	// "file still there" would be asserting a disk state nothing checked.
	if fresh.prunable > 0 {
		fmt.Printf("  in ledger, not transcript %d  (`loom report` deletes these rows)\n", fresh.prunable)
	}
	if fresh.outsideRoot > 0 {
		fmt.Printf("  in ledger, outside root   %d  (nothing to do; the root is an argument)\n", fresh.outsideRoot)
	}
	if fresh.unexpected > 0 {
		fmt.Printf("  in ledger, unexpected     %d  (under the root, but not a path this version walks)\n", fresh.unexpected)
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

type freshnessCounts struct {
	onDisk, current, stale, unseen int
	// A file Walk returned that could not be stat'd, so its size cannot be
	// compared. Counted so current+stale+unseen+this equals onDisk; a state
	// that vanishes from a set of totals that are read against each other is
	// worse than one with an awkward name.
	unreadableOnDisk int
	// The five ways a ledger row can fail to come back from Walk. See the
	// classifier in freshness for why they are counted apart.
	missing, unreadable, prunable, outsideRoot, unexpected int
}

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
		// A projects root that does not exist at all is a normal state for a
		// new install, and the ledger rows are still worth classifying: nothing
		// was walked, so every one of them is genuinely not-walked and the loop
		// below is as accurate as it ever is.
		//
		// Every other walk failure is different in kind and must not take the
		// same path. WalkDir reports a directory removed mid-walk as an ENOENT
		// too, and there `paths` is partial: continuing would classify live,
		// current transcripts as unexpected, and the old code went further and
		// returned zeroes for everything - printing "0 transcripts on disk, 0
		// ingested" against a ledger holding hundreds of rows, which reads as
		// confirmed-empty rather than as a failed scan. So distinguish the two
		// by asking about the root itself, and fail loudly for the rest.
		if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
			return f, err
		}
		paths = nil
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
			f.unreadableOnDisk++
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
		// "Walk did not return it" is five different states, and the whole
		// value of this block is telling them apart - each one implies a
		// different action, and two of them imply none at all. Every bucket is
		// keyed on its own positive signal, and nothing is inferred from what
		// is left over:
		//
		//   - not a path Walk would ever ingest, and `loom report` deletes it:
		//     the one case where naming a remedy is honest, so it is keyed on
		//     the same predicate the prune uses.
		//   - genuinely absent: the row outlived its file.
		//   - present but unreadable: says nothing about the data, and must
		//     not be reported as absence.
		//   - outside the root being asked about: normal, nothing to do, and
		//     decided by comparing against the root rather than by exhaustion.
		//   - under the root, readable, and still not walked: no story explains
		//     it, so it gets a bucket that says exactly that.
		//
		// The out-of-root case is why this cannot be inferred from Walk's
		// silence at all: the root is an argument, and the ledger deliberately
		// holds runs from outside it (that is what an empty lane means).
		// Guessing from absence reported 221 real transcripts as prunable
		// against a narrowed root. The last bucket is the same lesson applied
		// once more: a row like <root>/sess/subagents/workflows/wf-1/other.jsonl
		// is neither prunable nor outside the root, and calling it either would
		// be a false statement about a row that never clears.
		if isPrunableLedgerPath(p) {
			f.prunable++
			continue
		}
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				f.missing++
			} else {
				f.unreadable++
			}
			continue
		}
		if outsideRoot(root, p) {
			f.outsideRoot++
			continue
		}
		f.unexpected++
	}
	return f, nil
}

// outsideRoot reports whether p lies outside root. An unresolvable comparison
// (different volumes, one side relative) counts as outside: the question this
// answers is "can the root being asked about explain why Walk skipped it", and
// a path the root cannot even be compared against is not explained by it.
func outsideRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
