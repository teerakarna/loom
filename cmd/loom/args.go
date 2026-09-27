package main

import (
	"fmt"
	"strings"
)

// parseArgs is the shared arg parser for report, status, context, and
// propose's own trailing --lane check (issue #94): each accepts zero or
// more "--flag value" pairs named in knownFlags, plus at most one bare
// positional argument when allowPositional is true. Anything else - an
// unrecognized flag, a bare argument when none is allowed, or a second bare
// argument - is rejected with usage, in one consistent format, instead of
// each command hand-rolling its own version of this check with different
// wording and different gaps (found by code review on issue #92's fix,
// which closed this gap for report alone: status had no validation
// whatsoever, and context silently ignored anything it didn't recognize
// rather than rejecting it).
//
// positionalGiven, not positional != "", is how a caller checks whether a
// positional argument was actually given: an explicit empty string
// ("loom report \"\"") and "nothing given" both leave positional == "", and
// collapsing the two made an accidentally-empty argument from a script
// silently fall back to the default root instead of erroring or at least
// visibly walking an empty path (found by code review).
//
// hints carries an optional per-flag suffix for the "needs a value" error
// only (e.g. "--lane": "see `loom status` for the lanes in your ledger") -
// the unknown-flag and extra-positional errors always use the same usage
// string, since there is nothing flag-specific to say there. A flag with no
// entry in hints (or a nil hints) falls back to usage.
//
// propose's apply/dismiss dispatch is not routed through this: a bare
// "apply"/"dismiss" selects a whole different argument shape, an id rather
// than a flag, which this function's grammar (one optional positional plus
// flags) cannot express. Once that dispatch has run and returned, though,
// what's left of propose's own grammar is exactly this function's shape -
// see runPropose, which calls this directly for that remainder.
func parseArgs(args []string, usage string, allowPositional bool, hints map[string]string, knownFlags ...string) (positional string, positionalGiven bool, flagValues map[string]string, err error) {
	flagValues = map[string]string{}
	known := map[string]bool{}
	for _, f := range knownFlags {
		known[f] = true
	}
	for i := 0; i < len(args); i++ {
		if known[args[i]] {
			if i+1 >= len(args) {
				hint := hints[args[i]]
				if hint == "" {
					hint = usage
				}
				return "", false, nil, fmt.Errorf("%s needs a value (%s)", args[i], hint)
			}
			flagValues[args[i]] = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			return "", false, nil, fmt.Errorf("%s (unknown flag %q)", usage, args[i])
		}
		if !allowPositional {
			return "", false, nil, fmt.Errorf("%s (unexpected argument %q)", usage, args[i])
		}
		if positionalGiven {
			return "", false, nil, fmt.Errorf("%s (unexpected second argument %q)", usage, args[i])
		}
		positional = args[i]
		positionalGiven = true
	}
	return positional, positionalGiven, flagValues, nil
}
