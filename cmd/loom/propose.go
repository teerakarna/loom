package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/teerakarna/loom/internal/asset"
	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

// filterPendingByLane keeps every proposal whose kind isn't lane-scoped
// (pin_model, revert_policy, retire_asset - shown regardless), plus any
// lane-scoped proposal (propose.LaneScopedKinds) that touches lane.
func filterPendingByLane(pending []ledger.ProposalRow, lane string) []ledger.ProposalRow {
	out := make([]ledger.ProposalRow, 0, len(pending))
	for _, p := range pending {
		if !propose.LaneScopedKinds[p.Kind] || slices.Contains(propose.EvidenceStores(p.Evidence), lane) {
			out = append(out, p)
		}
	}
	return out
}

func runPropose(args []string) error {
	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if len(args) > 0 && args[0] == "apply" {
		if len(args) != 2 {
			return fmt.Errorf("usage: loom propose apply <id>")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a proposal id", args[1])
		}
		msg, err := propose.Apply(db, id, time.Now())
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	}

	if len(args) > 0 && args[0] == "dismiss" {
		if len(args) != 2 {
			return fmt.Errorf("usage: loom propose dismiss <id>")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a proposal id", args[1])
		}
		if err := db.DismissProposal(id); err != nil {
			return err
		}
		fmt.Printf("Dismissed #%d. It will not come back unless the evidence behind it changes.\n", id)
		return nil
	}
	// `loom propose --lane <lane>` narrows the four memory-finding kinds
	// (store-scoped by construction) to one project's own store.
	// pin_model/revert_policy/retire_asset are shown regardless: the policy
	// they write is machine-global, so filtering them by lane would imply a
	// per-lane policy that does not exist (issue #68).
	var lane string
	for i := 0; i < len(args); i++ {
		if args[i] == "--lane" {
			if i+1 >= len(args) {
				return fmt.Errorf("--lane needs a value (see `loom status` for the lanes in your ledger)")
			}
			lane = args[i+1]
			i++
			continue
		}
		return fmt.Errorf("unknown argument %q (want: apply, dismiss, --lane <lane>, or nothing to list)", args[i])
	}

	now := time.Now()
	generated, err := propose.Generate(db, now)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	memoryFindings, scannedStores, err := propose.GenerateMemoryFindings(home)
	if err != nil {
		return err
	}
	generated = append(generated, memoryFindings...)
	// Store always sees the full, unfiltered set, lane or no lane: Store
	// withdraws any pending proposal the generated set it's given does not
	// contain (issue #40), so passing it a lane-filtered subset would
	// wrongly withdraw every other lane's still-valid proposals as a side
	// effect of narrowing this one invocation's own display.
	if _, err := propose.Store(db, generated, scannedStores, now); err != nil {
		return err
	}

	pending, err := db.ListProposals(true)
	if err != nil {
		return err
	}
	totalPending := len(pending)
	if lane != "" {
		pending = filterPendingByLane(pending, lane)
	}
	if len(pending) == 0 {
		if lane != "" && totalPending > 0 {
			// Found by code review, before this shipped: the generic
			// "nothing in the ledger" message below is false here - there
			// are totalPending proposals, just none for this lane. Left
			// unqualified, a reader would reasonably conclude loom found no
			// evidence at all rather than that the lane filter excluded
			// everything.
			fmt.Printf("No proposals for lane %s.\n", ingest.LaneDisplay(lane))
			fmt.Println()
			fmt.Printf("%d proposal(s) exist for other lanes, or aren't lane-scoped. Drop --lane to see them.\n", totalPending)
			return nil
		}
		fmt.Println("No proposals.")
		fmt.Println()
		fmt.Println("Nothing in the ledger currently supports one. A proposal with no evidence behind")
		fmt.Println("it is a guess with extra ceremony, so loom would rather say nothing.")
		return nil
	}

	if lane != "" {
		fmt.Printf("Lane:            %s\n", ingest.LaneDisplay(lane))
		fmt.Println("(memory-finding kinds only - pin_model/revert_policy/retire_asset are shown")
		fmt.Println(" regardless, since the policy they write is not lane-scoped)")
		fmt.Println()
	}

	fmt.Printf("%d proposal(s):\n\n", len(pending))
	for _, p := range pending {
		var ev map[string]any
		_ = json.Unmarshal([]byte(p.Evidence), &ev)

		fmt.Printf("  #%-3d %s\n", p.ID, summaryFor(p, ev))
		if propose.TouchesUserFiles(p.Kind) {
			fmt.Printf("       loom will NOT apply this: it touches your files. Review and act yourself.\n")
		} else {
			fmt.Printf("       loom's own state only. `loom propose apply %d` to take it; reverts in one command.\n", p.ID)
		}
		if p.SampleSize > 0 {
			fmt.Printf("       evidence: %d run(s)\n", p.SampleSize)
		}
		fmt.Printf("       %s\n", compactEvidence(ev))
		fmt.Println()
	}
	fmt.Println("Dismiss with: loom propose dismiss <id>")
	fmt.Println("A dismissal holds until the evidence behind it changes, not until an interval elapses.")
	return nil
}

// summaryFor renders a one-line description from stored evidence. The summary
// is rebuilt at display time rather than stored, so changing the wording never
// requires rewriting rows.
func summaryFor(p ledger.ProposalRow, ev map[string]any) string {
	switch p.Kind {
	case propose.KindRetireAsset:
		return fmt.Sprintf("retire %v %q, unused for %v days",
			ev["type"], ev["name"], ev["days_unused"])
	case propose.KindPinModel:
		return fmt.Sprintf("pin %v to %v, measured over %v runs",
			ev["agent_type"], ev["observed_model"], ev["runs"])
	case propose.KindRevertPolicy:
		return fmt.Sprintf("revert %v: %v", ev["agent_type"], ev["reason"])
	case propose.KindPromoteMemoryDuplicate:
		return fmt.Sprintf("promote %q to a reference skill, identical across %v stores",
			ev["filename"], ev["stores"])
	case propose.KindBrokenLink:
		if ev["target_slug"] == asset.MemoryIndexSlug {
			return fmt.Sprintf("%v links to [[MEMORY]], but this store has no MEMORY.md", ev["filename"])
		}
		return fmt.Sprintf("%v links to [[%v]], which exists but not in this store",
			ev["filename"], ev["target_slug"])
	case propose.KindUnreachableAsset:
		return fmt.Sprintf("%v exists but is not linked from its store's MEMORY.md", ev["filename"])
	case propose.KindFilenameSlugDrift:
		return fmt.Sprintf("%v's filename no longer matches its own name: %v", ev["filename"], ev["slug"])
	default:
		return fmt.Sprintf("%s: %s", p.Kind, p.Subject)
	}
}

// compactEvidence prints the evidence without the noise of raw JSON, so a
// reader can judge the proposal rather than take it on trust.
func compactEvidence(ev map[string]any) string {
	switch {
	case ev["path"] != nil:
		state := "still on disk"
		if onDisk, ok := ev["on_disk"].(bool); ok && !onDisk {
			state = "gone from disk"
		}
		everUsed := "never used"
		if u, ok := ev["ever_used"].(bool); ok && u {
			everUsed = "used before"
		}
		return fmt.Sprintf("%v (%s, %s)", ev["path"], state, everUsed)
	case ev["since_median_cost"] != nil:
		return fmt.Sprintf("applied %v, %v runs since; median cost %.0f -> %.0f, denials %.2f -> %.2f",
			ev["applied_at"], ev["runs_since"],
			asFloat(ev["baseline_median_cost"]), asFloat(ev["since_median_cost"]),
			asFloat(ev["baseline_denial_rate"]), asFloat(ev["since_denial_rate"]))
	case ev["median_cost"] != nil:
		return fmt.Sprintf("median cost %.0f, %.0f tool calls/run, %.2f denials/run",
			asFloat(ev["median_cost"]), asFloat(ev["median_tools"]), asFloat(ev["denial_rate"]))
	case ev["paths"] != nil:
		return fmt.Sprintf("%v", ev["paths"])
	case ev["store"] != nil && ev["filename"] != nil:
		return fmt.Sprintf("%v/%v", ev["store"], ev["filename"])
	default:
		return ""
	}
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
