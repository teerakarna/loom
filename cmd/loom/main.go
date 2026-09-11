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
  loom report [path]   Ingest transcripts under path (default ~/.claude/projects)
                        and print an aggregate cost/usage report.

See docs/design.md for the full design.`)
}
