package ingest

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gotLine struct {
	text     string
	oversize bool
}

func readAll(t *testing.T, in string, limit int) []gotLine {
	t.Helper()
	br := bufio.NewReaderSize(strings.NewReader(in), 16)
	var out []gotLine
	for {
		line, oversize, err := readBoundedLine(br, limit)
		if oversize || len(line) > 0 {
			out = append(out, gotLine{string(line), oversize})
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			return out
		}
	}
}

func TestReadBoundedLineDropsOnlyTheOversizeLine(t *testing.T) {
	got := readAll(t, "ok1\n"+strings.Repeat("x", 100)+"\nok2\n", 10)
	want := []gotLine{{"ok1", false}, {"", true}, {"ok2", false}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The counterpoint: a line within the limit, and one exactly at it, must
// survive, including across the reader's own 16-byte buffer boundary.
func TestReadBoundedLineKeepsLinesWithinTheLimit(t *testing.T) {
	exact := strings.Repeat("y", 40)
	got := readAll(t, exact+"\nlast-without-newline", 40)
	if len(got) != 2 || got[0] != (gotLine{exact, false}) || got[1] != (gotLine{"last-without-newline", false}) {
		t.Fatalf("got %+v", got)
	}
}

func TestIngestFileSurvivesAnOversizeLine(t *testing.T) {
	line := func(id string) string {
		return `{"type":"assistant","sessionId":"s1","timestamp":"2026-01-01T00:00:00Z","message":{"id":"` + id + `","role":"assistant","model":"claude-sonnet-5","usage":{"output_tokens":7}}}` + "\n"
	}
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("z", maxLineSize+1) + `"}}` + "\n"
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(line("a")+huge+line("b")), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := IngestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rs.OversizeLines != 1 {
		t.Errorf("OversizeLines = %d, want 1", rs.OversizeLines)
	}
	if rs.Usage.OutputTokens != 14 {
		t.Errorf("OutputTokens = %d, want 14: lines either side of the oversize one must both count", rs.Usage.OutputTokens)
	}
}
