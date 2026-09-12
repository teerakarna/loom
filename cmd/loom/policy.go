package main

import (
	"fmt"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/policy"
)

func runPolicy(args []string) error {
	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if len(args) == 0 {
		return showPolicies(db)
	}

	switch args[0] {
	case "set":
		if len(args) != 4 {
			return fmt.Errorf("usage: loom policy set <agent-type> <model> <effort>")
		}
		return setPolicy(db, args[1], args[2], args[3])
	case "unset":
		if len(args) != 2 {
			return fmt.Errorf("usage: loom policy unset <agent-type>")
		}
		if err := db.DeletePolicy(args[1]); err != nil {
			return err
		}
		fmt.Printf("Removed the stored policy for %q. It now resolves to the shipped default.\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (want: set, unset, or nothing to show)", args[0])
	}
}

func setPolicy(db *ledger.DB, agentType, model, effort string) error {
	if err := db.UpsertPolicy(ledger.PolicyRow{
		CriteriaVersion: policy.CriteriaVersion,
		AgentType:       agentType,
		Model:           model,
		Effort:          effort,
		Source:          "human",
	}, time.Now()); err != nil {
		return err
	}
	fmt.Printf("Set %s -> %s (effort %s).\n", agentType, model, effort)
	return nil
}

// showPolicies prints the effective decision for every agent type the ledger
// has seen, plus any type with a stored policy but no runs yet.
func showPolicies(db *ledger.DB) error {
	stats, err := db.StatsByAgentType()
	if err != nil {
		return err
	}

	byType := map[string]*ledger.AgentTypeStats{}
	var order []string
	for i := range stats {
		byType[stats[i].AgentType] = &stats[i]
		order = append(order, stats[i].AgentType)
	}

	if len(order) == 0 {
		fmt.Println("No agent runs ingested yet, so there is nothing measured to show.")
		fmt.Println("Run `loom report` first. Until then every agent type resolves to its shipped default.")
		return nil
	}

	fmt.Printf("Effective policy per agent type (criteria %s):\n\n", policy.CriteriaVersion)
	for _, at := range order {
		stored, err := db.GetPolicy(at)
		if err != nil {
			return err
		}
		d := policy.Resolve(at, stored, byType[at])

		marker := " "
		if d.TrustedEvidence() {
			marker = "*"
		}
		fmt.Printf("%s %-16s %-8s effort=%-7s [%s", marker, d.AgentType, d.Model, d.Effort, d.Source)
		if d.SampleSize > 0 {
			fmt.Printf(", n=%d", d.SampleSize)
		}
		fmt.Println("]")
		fmt.Printf("    %s\n", d.Rationale)

		if s := byType[at]; s != nil && s.Runs > 0 {
			fmt.Printf("    measured: median cost %.0f, median %.0f tool calls, %.2f denials/run, %.2f corrections/run\n",
				s.MedianCost, s.MedianTools, s.DenialRate, s.FeedbackRate)
		}
		fmt.Println()
	}

	fmt.Printf("Lines marked * rest on at least %d runs. Everything else is a shipped default or a\n", policy.MinSampleSize)
	fmt.Println("deliberate setting, not a measured finding. Medians, not means: run cost is heavily")
	fmt.Println("skewed, so a mean describes the outlier rather than the typical run.")
	return nil
}
