package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/teerakarna/loom/internal/artifact"
	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/selector"
)

// staleAfter is how long an artifact can go unseen by a discovery pass
// before runAdvise marks it stale. A single missed run shouldn't flip a
// still-real skill to stale — this only catches something that's been gone
// across several invocations.
const staleAfter = 30 * 24 * time.Hour

func runAdvise(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: loom advise <task description>")
	}
	text := strings.Join(args, " ")

	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := discoverAndUpsert(db); err != nil {
		return err
	}

	artifacts, err := db.ListArtifacts()
	if err != nil {
		return err
	}
	rec := selector.Recommend(selector.TaskDescriptor{Text: text}, activeOnly(artifacts))

	printRecommendation(rec)
	return nil
}

// discoverAndUpsert scans the standard Claude Code locations plus the
// current project, upserts everything found into the ledger, and marks
// anything not seen for staleAfter as stale. Run before every recommendation
// so `loom advise` always reflects what's actually on disk right now — see
// docs/design.md design constraint 2, "Discover, never assume."
func discoverAndUpsert(db *ledger.DB) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	found, err := artifact.Discover(artifact.DefaultLocations(home, cwd))
	if err != nil {
		return err
	}

	now := time.Now()
	for _, a := range found {
		rec := ledger.ArtifactRecord{Kind: a.Kind, Path: a.Path, Name: a.Name, Description: a.Description}
		if err := db.UpsertArtifact(rec, now); err != nil {
			return err
		}
	}
	return db.MarkStaleArtifacts(now.Add(-staleAfter))
}

func activeOnly(rows []ledger.ArtifactRow) []ledger.ArtifactRow {
	var out []ledger.ArtifactRow
	for _, r := range rows {
		if r.Status == "active" {
			out = append(out, r)
		}
	}
	return out
}

func printRecommendation(rec selector.Recommendation) {
	fmt.Printf("Model:  %s (effort: %s)\n", rec.Model, rec.Effort)
	fmt.Printf("Why:    %s\n", rec.Rationale)
	if len(rec.Matches) == 0 {
		fmt.Println("\nNo matching skills/agents/plans found.")
		return
	}
	fmt.Println("\nRelevant artifacts:")
	for _, m := range rec.Matches {
		fmt.Printf("  [%.2f] %-8s %-30s %s\n", m.Score, m.Kind, m.Name, m.Description)
	}
}
