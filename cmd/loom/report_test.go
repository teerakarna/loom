package main

import (
	"strings"
	"testing"
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
