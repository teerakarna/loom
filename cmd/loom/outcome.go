package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
)

// runRecordOutcome is the CLI counterpart to what used to be the MCP tool
// record_outcome - moved here so a live session can record one via Bash
// without loom's MCP surface carrying a write path. See docs/design.md,
// "MCP server shape".
const recordOutcomeUsage = "usage: loom record-outcome --outcome <accepted|corrected|rejected> [--detail <text>] <task text>"

// parseRecordOutcomeArgs is split out from runRecordOutcome so the parsing
// logic can be tested without touching the real ledger (cmd/loom has no way
// to inject a test ledger path - defaultLedgerPath always resolves
// ~/.loom/loom.db).
func parseRecordOutcomeArgs(args []string) (outcome, detail, taskText string, err error) {
	// Flags must precede the task text (the usage string says so): once the
	// first non-flag token appears, parsing stops treating "--outcome" or
	// "--detail" specially, so a task description that happens to contain
	// those words verbatim is never misread as a flag.
	i := 0
	for ; i < len(args); i++ {
		if args[i] == "--outcome" {
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--outcome needs a value")
			}
			outcome = args[i+1]
			i++
			continue
		}
		if args[i] == "--detail" {
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--detail needs a value")
			}
			detail = args[i+1]
			i++
			continue
		}
		break
	}
	rest := args[i:]

	switch outcome {
	case "accepted", "corrected", "rejected":
	default:
		return "", "", "", fmt.Errorf("%s\noutcome must be one of: accepted, corrected, rejected (got %q)", recordOutcomeUsage, outcome)
	}
	if len(rest) == 0 {
		return "", "", "", fmt.Errorf("%s", recordOutcomeUsage)
	}
	return outcome, detail, strings.Join(rest, " "), nil
}

func runRecordOutcome(args []string) error {
	outcome, detail, taskText, err := parseRecordOutcomeArgs(args)
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

	payload := map[string]string{"task_text": taskText, "outcome": outcome, "detail": detail}
	if err := db.InsertEvent(time.Now(), "", "outcome", payload); err != nil {
		return err
	}
	fmt.Println("recorded")
	return nil
}
