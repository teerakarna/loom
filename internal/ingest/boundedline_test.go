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

func TestReadBoundedLineBoundaries(t *testing.T) {
	big := strings.Repeat("a", 41)
	cases := []struct {
		name, in string
		want     []gotLine
	}{
		{"oversize at EOF without newline", "k\n" + big, []gotLine{{"k", false}, {"", true}}},
		{"one over, with newline", big + "\nnext\n", []gotLine{{"", true}, {"next", false}}},
		{"one over, no newline", big, []gotLine{{"", true}}},
		{"exactly at limit, no newline", big[:40], []gotLine{{big[:40], false}}},
		{"CRLF stripped", "a\r\nb\r\n", []gotLine{{"a", false}, {"b", false}}},
		{"oversize spanning buffers then normal", strings.Repeat("q", 500) + "\nok\n", []gotLine{{"", true}, {"ok", false}}},
	}
	for _, c := range cases {
		got := readAll(t, c.in, 40)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s line %d: got %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}
