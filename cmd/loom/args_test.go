package main

import (
	"strings"
	"testing"
)

func TestParseArgsAcceptsKnownFlagAndPositional(t *testing.T) {
	positional, flags, err := parseArgs([]string{"/some/path", "--lane", "work"}, "usage: x", true, "--lane")
	if err != nil {
		t.Fatal(err)
	}
	if positional != "/some/path" {
		t.Errorf("positional = %q, want /some/path", positional)
	}
	if flags["--lane"] != "work" {
		t.Errorf("flags[--lane] = %q, want work", flags["--lane"])
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	_, _, err := parseArgs([]string{"--lanes", "work"}, "usage: x", true, "--lane")
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--lanes"`) {
		t.Errorf("err = %v, want it to name the unknown flag", err)
	}
}

func TestParseArgsRejectsSecondPositional(t *testing.T) {
	_, _, err := parseArgs([]string{"/a", "/b"}, "usage: x", true)
	if err == nil || !strings.Contains(err.Error(), `unexpected second argument "/b"`) {
		t.Errorf("err = %v, want it to name the unexpected second argument", err)
	}
}

func TestParseArgsRejectsAnyPositionalWhenNoneAllowed(t *testing.T) {
	_, _, err := parseArgs([]string{"myproject"}, "usage: x", false)
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "myproject"`) {
		t.Errorf("err = %v, want it to reject the bare argument outright", err)
	}
}

func TestParseArgsRejectsFlagMissingItsValue(t *testing.T) {
	_, _, err := parseArgs([]string{"--lane"}, "usage: x", true, "--lane")
	if err == nil || !strings.Contains(err.Error(), "--lane needs a value") {
		t.Errorf("err = %v, want it to say --lane needs a value", err)
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
