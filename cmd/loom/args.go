package main

import (
	"fmt"
	"strings"
)

// parseArgs is the shared arg parser for report, status, and context (issue
// #94): each accepts zero or more "--flag value" pairs named in knownFlags,
// plus at most one bare positional argument when allowPositional is true.
// Anything else - an unrecognized flag, a bare argument when none is
// allowed, or a second bare argument - is rejected with usage, in one
// consistent format, instead of each command hand-rolling its own version
// of this check with different wording and different gaps (found by code
// review on issue #92's fix, which closed this gap for report alone: status
// had no validation whatsoever, and context silently ignored anything it
// didn't recognize rather than rejecting it).
//
// propose is not unified through this: its shape is dispatch-first (the
// bare word "apply"/"dismiss" selects a whole different argument shape, an
// id, rather than a flag), not "one optional positional plus flags" -
// forcing it through this signature would need a positional-then-subcommand
// split this function does not do, for one caller with a genuinely
// different grammar. It keeps its own parsing, with matching wording (see
// runPropose).
func parseArgs(args []string, usage string, allowPositional bool, knownFlags ...string) (positional string, flagValues map[string]string, err error) {
	flagValues = map[string]string{}
	known := map[string]bool{}
	for _, f := range knownFlags {
		known[f] = true
	}
	positionalSet := false
	for i := 0; i < len(args); i++ {
		if known[args[i]] {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s needs a value (%s)", args[i], usage)
			}
			flagValues[args[i]] = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			return "", nil, fmt.Errorf("%s (unknown flag %q)", usage, args[i])
		}
		if !allowPositional {
			return "", nil, fmt.Errorf("%s (unexpected argument %q)", usage, args[i])
		}
		if positionalSet {
			return "", nil, fmt.Errorf("%s (unexpected second argument %q)", usage, args[i])
		}
		positional = args[i]
		positionalSet = true
	}
	return positional, flagValues, nil
}
