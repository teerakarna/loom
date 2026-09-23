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
func runRecordOutcome(args []string) error {
	const usage = "usage: loom record-outcome --outcome <accepted|corrected|rejected> [--detail <text>] <task text>"

	var outcome, detail string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--outcome":
			if i+1 >= len(args) {
				return fmt.Errorf("--outcome needs a value")
			}
			i++
			outcome = args[i]
		case "--detail":
			if i+1 >= len(args) {
				return fmt.Errorf("--detail needs a value")
			}
			i++
			detail = args[i]
		default:
			rest = append(rest, args[i])
		}
	}

	switch outcome {
	case "accepted", "corrected", "rejected":
	default:
		return fmt.Errorf("%s\noutcome must be one of: accepted, corrected, rejected (got %q)", usage, outcome)
	}
	if len(rest) == 0 {
		return fmt.Errorf("%s", usage)
	}
	taskText := strings.Join(rest, " ")

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
