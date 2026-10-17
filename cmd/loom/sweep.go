package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

// runSweep groups pending proposals into what can be cleaned up in one pass
// and what needs a decision. It prints and never writes: the safe group comes
// out as a script for the user to review and run, because loom does not write
// to the user's files (docs/design.md, B5 scope).
func runSweep(db *ledger.DB, args []string) error {
	scriptOnly := false
	for _, a := range args {
		if a != "--script" {
			return fmt.Errorf("usage: loom propose sweep [--script]")
		}
		scriptOnly = true
	}

	pending, err := refreshPending(db, time.Now())
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// Only an unreachable_asset item reads a slug, so only scan when one is
	// pending. A failed scan leaves titles as filenames, which is still valid.
	var slugs map[string]string
	for _, p := range pending {
		if p.Kind == propose.KindUnreachableAsset {
			slugs = propose.SlugIndex(home)
			break
		}
	}
	items := make([]propose.SweepItem, 0, len(pending))
	for _, p := range pending {
		var ev map[string]any
		_ = json.Unmarshal([]byte(p.Evidence), &ev)
		items = append(items, propose.Classify(home, p, ev, slugs))
	}

	if scriptOnly {
		_, err := io.WriteString(os.Stdout, propose.SweepScript(items))
		return err
	}
	printSweep(os.Stdout, items)
	return nil
}

func printSweep(w io.Writer, items []propose.SweepItem) {
	var safe, decide []propose.SweepItem
	for _, it := range items {
		if it.Tier == propose.TierSafe {
			safe = append(safe, it)
		} else {
			decide = append(decide, it)
		}
	}
	if len(items) == 0 {
		_, _ = fmt.Fprintln(w, "No proposals.")
		return
	}

	_, _ = fmt.Fprintf(w, "Safe to sweep: %d\n", len(safe))
	if len(safe) == 0 {
		_, _ = fmt.Fprintln(w, "  nothing passed its checks")
	}
	for _, it := range safe {
		_, _ = fmt.Fprintf(w, "  #%-3d %s\n       adds: %s\n", it.ID, it.Summary, it.Line)
	}
	if len(safe) > 0 {
		_, _ = fmt.Fprintln(w, "\n  Each one only adds a line, re-checked against the files just now.")
		_, _ = fmt.Fprintln(w, "  Loom will not run it. To do the sweep:")
		_, _ = fmt.Fprintln(w, "    loom propose sweep --script > sweep.sh   # read it, then: sh sweep.sh")
	}

	if len(decide) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\nNeeds a decision: %d\n", len(decide))
	// Items with the same advice print it once: eight "only the separator
	// differs" findings read as one decision, not eight.
	var order []string
	groups := map[string][]propose.SweepItem{}
	for _, it := range decide {
		key := it.Why + "\x00" + it.BottomLine + "\x00" + strings.Join(it.Options, "\x00")
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], it)
	}
	for _, key := range order {
		g := groups[key]
		_, _ = fmt.Fprintln(w)
		for _, it := range g {
			_, _ = fmt.Fprintf(w, "  #%-3d %s\n", it.ID, it.Summary)
		}
		if g[0].Why != "" {
			_, _ = fmt.Fprintf(w, "       not swept: %s\n", g[0].Why)
		}
		_, _ = fmt.Fprintf(w, "       bottom line: %s\n", g[0].BottomLine)
		for _, o := range g[0].Options {
			_, _ = fmt.Fprintf(w, "         - %s\n", o)
		}
	}
}
