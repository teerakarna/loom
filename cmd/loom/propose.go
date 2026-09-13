package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

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
	if len(args) > 0 {
		return fmt.Errorf("unknown subcommand %q (want: dismiss, or nothing to list)", args[0])
	}

	now := time.Now()
	generated, err := propose.Generate(db, now)
	if err != nil {
		return err
	}
	if _, err := propose.Store(db, generated, now); err != nil {
		return err
	}

	pending, err := db.ListProposals(true)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Println("No proposals.")
		fmt.Println()
		fmt.Println("Nothing in the ledger currently supports one. A proposal with no evidence behind")
		fmt.Println("it is a guess with extra ceremony, so loom would rather say nothing.")
		return nil
	}

	fmt.Printf("%d proposal(s):\n\n", len(pending))
	for _, p := range pending {
		var ev map[string]any
		_ = json.Unmarshal([]byte(p.Evidence), &ev)

		fmt.Printf("  #%-3d %s\n", p.ID, summaryFor(p, ev))
		if propose.TouchesUserFiles(p.Kind) {
			fmt.Printf("       loom will NOT apply this: it touches your files. Review and act yourself.\n")
		} else {
			fmt.Printf("       applies to loom's own state only, and reverts in one command.\n")
		}
		if p.SampleSize > 0 {
			fmt.Printf("       evidence: %d run(s)\n", p.SampleSize)
		}
		fmt.Printf("       %s\n", compactEvidence(ev))
		fmt.Println()
	}
	fmt.Println("`loom propose dismiss <id>` to dismiss one. A dismissal holds until the evidence")
	fmt.Println("behind it changes, not until some interval elapses.")
	return nil
}

// summaryFor renders a one-line description from stored evidence. The summary
// is rebuilt at display time rather than stored, so changing the wording never
// requires rewriting rows.
func summaryFor(p ledger.ProposalRow, ev map[string]any) string {
	switch p.Kind {
	case propose.KindRetireArtifact:
		return fmt.Sprintf("retire %v %q, unseen for %v days",
			ev["type"], ev["name"], ev["days_unseen"])
	case propose.KindPinModel:
		return fmt.Sprintf("pin %v to %v, measured over %v runs",
			ev["agent_type"], ev["observed_model"], ev["runs"])
	default:
		return fmt.Sprintf("%s: %s", p.Kind, p.Subject)
	}
}

// compactEvidence prints the evidence without the noise of raw JSON, so a
// reader can judge the proposal rather than take it on trust.
func compactEvidence(ev map[string]any) string {
	switch {
	case ev["path"] != nil:
		return fmt.Sprintf("%v (last seen %v)", ev["path"], ev["last_seen"])
	case ev["median_cost"] != nil:
		return fmt.Sprintf("median cost %.0f, %.0f tool calls/run, %.2f denials/run",
			asFloat(ev["median_cost"]), asFloat(ev["median_tools"]), asFloat(ev["denial_rate"]))
	default:
		return ""
	}
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
