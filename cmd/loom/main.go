// Command loom is the CLI entry point. See docs/design.md for the full design;
// nothing below B1 (ingest, ledger, `loom report`) is implemented yet.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "loom: not yet implemented — see docs/design.md")
	os.Exit(1)
}
