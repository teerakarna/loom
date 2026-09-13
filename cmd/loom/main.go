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
	case "propose":
		err = runPropose(os.Args[2:])
	case "policy":
		err = runPolicy(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, `loom — artifact lifecycle and cost/routing engine for Claude Code

Usage:
  loom report [path] [--lane <lane>]
                            Ingest transcripts under path (default ~/.claude/projects)
                             and print an aggregate cost/usage report, optionally
                             narrowed to one lane (the project directory a session ran in).
  loom advise <task text>   Discover skills/agents/plans/hooks, and recommend which
                             are relevant plus a cold-start model/effort choice.
  loom status [path]        Report on loom itself: freshness, what it knows, and what
                             it cannot answer. Read-only, never ingests.
  loom propose              List proposals loom's evidence supports, with what each
                             rests on. Add "dismiss <id>" to dismiss one.
  loom policy               Show the effective model/effort per agent type, with the
                             source and sample size behind each decision.
  loom policy set <agent-type> <model> <effort>
  loom policy unset <agent-type>
  loom policy render        Write agent definitions for pinned models into
                             ~/.loom/generated/agents/ (never installed for you).
  loom serve                Run the MCP server on stdio (query_ledger, get_recommendation,
                             list_proposals, record_outcome).

See docs/design.md for the full design.`)
}
