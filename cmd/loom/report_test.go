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

func TestDescribeRunSpanWording(t *testing.T) {
	cases := []struct {
		hours float64
		want  string
	}{
		{0.4, ""},
		{1.2, "alive about an hour"},
		{5, "alive 5 hours"},
		{47.5, "alive 48 hours"},
		{48, "alive 2 days"},
		{1488, "alive 62 days"},
	}
	for _, c := range cases {
		got := describeRun(ledger.RunCost{ToolUses: 1, SpanHours: c.hours})
		if c.want == "" && strings.Contains(got, "alive") {
			t.Errorf("%vh: printed a span %q, want none under an hour", c.hours, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%vh: %q, want %q", c.hours, got, c.want)
		}
		if strings.Contains(got, "1 hours") {
			t.Errorf("%vh: bad plural in %q", c.hours, got)
		}
	}
}
