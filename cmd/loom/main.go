// Command loom is the CLI entry point. See docs/design.md for the full
// design; only B1 (ingest, ledger, `loom report`) is implemented so far.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "report":
		err = runReport(os.Args[2:])
	case "advise":
		err = runAdvise(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "context":
		err = runContext(os.Args[2:])
	case "propose":
		err = runPropose(os.Args[2:])
	case "policy":
		err = runPolicy(os.Args[2:])
	case "record-outcome":
		err = runRecordOutcome(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "loom:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `loom - asset lifecycle and cost/routing engine for Claude Code

Usage:
  loom report [path] [--lane <lane>]
                            Ingest transcripts under path (default ~/.claude/projects)
                             and print an aggregate cost/usage report, optionally
                             narrowed to one lane (the project directory a session ran in).
  loom advise [--agent-type <type>] <task text>
                             Discover skills/agents/plans/hooks, and recommend which
                             are relevant plus a model/effort choice. With --agent-type,
                             uses that type's real policy (loom policy) when one exists,
                             instead of a keyword-only cold-start guess.
  loom status [path]        Report on loom itself: freshness, what it knows, and what
                             it cannot answer. Read-only, never ingests.
  loom context [--lane <lane>]
                             Report on context occupancy: what filled the window (tool
                             output, by tool and bucket) and what compaction cost.
                             Read-only, never ingests - run "loom report" first.
  loom propose [--lane <lane>]
                             List proposals loom's evidence supports, with what each
                             rests on. Add "apply <id>" or "dismiss <id>" to act on one.
                             --lane narrows the memory-finding kinds to one project's
                             own store; pin_model/revert_policy/retire_asset always
                             show, since the policy they write is not lane-scoped.
  loom policy               Show the effective model/effort per agent type, with the
                             source and sample size behind each decision.
  loom policy set <agent-type> <model> <effort>
  loom policy unset <agent-type>
  loom policy render        Write agent definitions for pinned models into
                             ~/.loom/generated/agents/ (never installed for you).
  loom record-outcome --outcome <accepted|corrected|rejected> [--detail <text>] <task text>
                             Record how a task turned out, for future selector tuning.
                             Write-only.
  loom serve                Run the MCP server on stdio, exposing get_recommendation,
                             get_cost_summary, get_context_occupancy and list_proposals.
                             dismiss_proposal and record_outcome stay CLI-only (propose
                             dismiss, record-outcome) - see docs/design.md,
                             "MCP surface widened back".

See docs/design.md for the full design.`)
}
