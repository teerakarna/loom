package main

import (
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

func TestMediaNoteOnlyAppearsWhenThereIsMedia(t *testing.T) {
	if got := mediaNote(ledger.ToolOutputRow{ResultBytes: 5000}); got != "" {
		t.Errorf("no media should print nothing, got %q", got)
	}
	got := mediaNote(ledger.ToolOutputRow{ResultBytes: 5 << 20, MediaBytes: 3 << 20})
	if !strings.Contains(got, "3.0 MiB") || !strings.Contains(got, "of which") || !strings.Contains(got, "images/documents") {
		t.Errorf("media note = %q", got)
	}
}
