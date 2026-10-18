package main

import (
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

func TestRunReportRejectsUnknownFlag(t *testing.T) {
	err := runReport([]string{"--lanes", "work"})
	if err == nil {
		t.Fatal("expected an error for an unrecognised flag, got nil")
	}
	if got := err.Error(); !strings.Contains(got, `unknown flag "--lanes"`) {
		t.Errorf("error = %q, want it to name the unknown flag", got)
	}
}

func TestRunReportRejectsHelpAsUsage(t *testing.T) {
	err := runReport([]string{"--help"})
	if err == nil {
		t.Fatal("expected an error for --help, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "usage: loom report") {
		t.Errorf("error = %q, want a usage string rather than an lstat error", got)
	}
}

func TestRunReportRejectsSecondBareRoot(t *testing.T) {
	err := runReport([]string{"/a", "/b"})
	if err == nil {
		t.Fatal("expected an error for a second bare argument, got nil")
	}
	if got := err.Error(); !strings.Contains(got, `unexpected second argument "/b"`) {
		t.Errorf("error = %q, want it to name the unexpected second argument", got)
	}
}

func TestDescribeRun(t *testing.T) {
	got := describeRun(ledger.RunCost{ToolUses: 5475, SpanHours: 1488, Compactions: 7, MediaBytes: 2 << 20})
	for _, want := range []string{"5475 tool calls", "alive 62 days", "compacted 7x", "2.0 MiB images/documents"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeRun = %q, missing %q", got, want)
		}
	}
	if got := describeRun(ledger.RunCost{}); got != "" {
		t.Errorf("a run with nothing to say printed %q", got)
	}
	if got := describeRun(ledger.RunCost{ToolUses: 3, SpanHours: 5}); strings.Contains(got, "compacted") || strings.Contains(got, "images") {
		t.Errorf("absent facts must not be printed: %q", got)
	}
	if got := describeRun(ledger.RunCost{ToolUses: 3, SpanHours: 5}); !strings.Contains(got, "alive 5 hours") {
		t.Errorf("a short span reads in hours: %q", got)
	}
}
