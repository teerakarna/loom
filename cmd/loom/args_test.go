package main

import (
	"strings"
	"testing"
)

func TestParseArgsAcceptsKnownFlagAndPositional(t *testing.T) {
	positional, positionalGiven, flags, err := parseArgs([]string{"/some/path", "--lane", "work"}, "usage: x", true, nil, "--lane")
	if err != nil {
		t.Fatal(err)
	}
	if !positionalGiven || positional != "/some/path" {
		t.Errorf("positional = (%q, %v), want (/some/path, true)", positional, positionalGiven)
	}
	if flags["--lane"] != "work" {
		t.Errorf("flags[--lane] = %q, want work", flags["--lane"])
	}
}

// TestParseArgsDistinguishesNoPositionalFromAnEmptyOne is the regression
// test for a finding from /code-review high: an earlier version returned
// the same positional == "" for "nothing given" and for an explicit empty
// string argument, so a caller checking positional != "" (as report.go and
// status.go both did) silently fell back to the default root for either
// case - masking a caller's own accidentally-empty argument instead of
// visibly acting on it or erroring.
func TestParseArgsDistinguishesNoPositionalFromAnEmptyOne(t *testing.T) {
	_, givenNone, _, err := parseArgs(nil, "usage: x", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if givenNone {
		t.Error("positionalGiven = true for no arguments at all, want false")
	}

	positional, givenEmpty, _, err := parseArgs([]string{""}, "usage: x", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !givenEmpty || positional != "" {
		t.Errorf("positional = (%q, %v), want (\"\", true) - an explicit empty string was still given", positional, givenEmpty)
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	_, _, _, err := parseArgs([]string{"--lanes", "work"}, "usage: x", true, nil, "--lane")
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--lanes"`) {
		t.Errorf("err = %v, want it to name the unknown flag", err)
	}
}

func TestParseArgsRejectsSecondPositional(t *testing.T) {
	_, _, _, err := parseArgs([]string{"/a", "/b"}, "usage: x", true, nil)
	if err == nil || !strings.Contains(err.Error(), `unexpected second argument "/b"`) {
		t.Errorf("err = %v, want it to name the unexpected second argument", err)
	}
}

func TestParseArgsRejectsAnyPositionalWhenNoneAllowed(t *testing.T) {
	_, _, _, err := parseArgs([]string{"myproject"}, "usage: x", false, nil)
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "myproject"`) {
		t.Errorf("err = %v, want it to reject the bare argument outright", err)
	}
}

func TestParseArgsRejectsFlagMissingItsValue(t *testing.T) {
	_, _, _, err := parseArgs([]string{"--lane"}, "usage: x", true, nil, "--lane")
	if err == nil || !strings.Contains(err.Error(), "--lane needs a value") {
		t.Errorf("err = %v, want it to say --lane needs a value", err)
	}
}

// TestParseArgsUsesHintOverUsageForMissingValue confirms a flag with an
// entry in hints gets that specific text, not the bare usage synopsis -
// found by code review: an earlier version dropped the discoverability hint
// each command's own hand-rolled "--lane needs a value" message used to
// carry ("see `loom status` for the lanes in your ledger"), leaving only
// the usage string.
func TestParseArgsUsesHintOverUsageForMissingValue(t *testing.T) {
	_, _, _, err := parseArgs([]string{"--lane"}, "usage: x", true,
		map[string]string{"--lane": "see `loom status` for the lanes in your ledger"}, "--lane")
	if err == nil || !strings.Contains(err.Error(), "see `loom status` for the lanes in your ledger") {
		t.Errorf("err = %v, want the specific hint, not just the bare usage string", err)
	}
}

// TestRunStatusRejectsUnknownFlag is the regression test for issue #94's
// first finding: a typo'd flag ("--lan" for "--lane", a flag status doesn't
// even have) used to be silently assigned into root, producing a confusing
// "could not scan" error naming a path nobody asked for, instead of an
// error naming the actual bad argument.
func TestRunStatusRejectsUnknownFlag(t *testing.T) {
	err := runStatus([]string{"--lan", "work"})
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--lan"`) {
		t.Errorf("err = %v, want it to name the unknown flag", err)
	}
}

// TestRunContextRejectsUnknownFlag is the regression test for issue #94's
// second finding: a typo'd "--lane" used to be silently ignored, so the
// command ran unscoped while the user believed it was narrowed to a lane -
// wrong output with no error at all.
func TestRunContextRejectsUnknownFlag(t *testing.T) {
	err := runContext([]string{"--lanes", "myproject"})
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--lanes"`) {
		t.Errorf("err = %v, want it to name the unknown flag instead of silently ignoring it", err)
	}
}

// TestRunContextRejectsABarePositional confirms context, which takes no
// path argument at all, rejects any bare argument rather than accepting
// it silently.
func TestRunContextRejectsABarePositional(t *testing.T) {
	err := runContext([]string{"myproject"})
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "myproject"`) {
		t.Errorf("err = %v, want it to reject the bare argument - context takes no positional", err)
	}
}

// TestRunProposeRejectsUnknownFlagLikeItsSiblings is the regression test
// for issue #94's third finding: propose's own trailing --lane check used
// different wording ("unknown argument") than report/status/context
// ("unknown flag") for the identical mistake, found by code review to have
// drifted again even after the first version of this fix, since it kept a
// second hand-written copy of the same loop instead of calling parseArgs.
//
// runPropose opens the ledger before validating args, unlike its siblings -
// HOME must point at a scratch dir, or this would touch the real ~/.loom.
func TestRunProposeRejectsUnknownFlagLikeItsSiblings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runPropose([]string{"--lan", "work"})
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--lan"`) {
		t.Errorf("err = %v, want the same \"unknown flag\" wording report/status/context use", err)
	}
}
