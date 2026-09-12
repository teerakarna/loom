package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	case "render":
		if len(args) != 1 {
			return fmt.Errorf("usage: loom policy render")
		}
		return renderPolicies(db)
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
		return fmt.Errorf("unknown subcommand %q (want: set, unset, render, or nothing to show)", args[0])
	}
}

// generatedAgentsDir is Loom's own output directory. Nothing is ever written
// to a client's agent directory: constraint 8 keeps Loom out of
// human-authored locations, and the human decides whether to adopt what is
// generated here.
func generatedAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".loom", "generated", "agents"), nil
}

func renderPolicies(db *ledger.DB) error {
	dir, err := generatedAgentsDir()
	if err != nil {
		return err
	}
	decisions, err := effectiveDecisions(db)
	if err != nil {
		return err
	}

	out, err := policy.Render(dir, decisions)
	if err != nil {
		return err
	}

	for _, w := range out.Written {
		fmt.Printf("wrote %s\n", w)
	}
	for _, s := range out.Skipped {
		fmt.Printf("skipped %s\n", s)
	}
	if len(out.Written) == 0 {
		fmt.Println()
		fmt.Println("Nothing rendered. A definition is only generated where there is something to")
		fmt.Println("pin beyond the shipped default: a deliberate policy, or evidence past the")
		fmt.Printf("%d-run threshold. Generating files that restate defaults would add material to\n", policy.MinSampleSize)
		fmt.Println("review without adding anything to review it against.")
		return nil
	}
	fmt.Println()
	fmt.Println("Loom does not install these. Copy one where your client reads agent definitions")
	fmt.Println("if you want it to take effect.")
	return nil
}

// effectiveDecisions resolves every agent type the ledger knows about, plus
// any type that has a stored policy but no runs yet.
func effectiveDecisions(db *ledger.DB) ([]policy.Decision, error) {
	stats, err := db.StatsByAgentType()
	if err != nil {
		return nil, err
	}
	var out []policy.Decision
	for i := range stats {
		stored, err := db.GetPolicy(stats[i].AgentType)
		if err != nil {
			return nil, err
		}
		out = append(out, policy.Resolve(stats[i].AgentType, stored, &stats[i]))
	}
	return out, nil
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
