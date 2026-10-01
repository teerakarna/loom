package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIngestFileStreamsRatherThanBuffering is the regression test for
// docs/design.md's Verification section, "Memory bound": ingest a large
// synthetic corpus under a hard ceiling, proving streaming. Generated at
// test time rather than checked in - a fixture this size would violate the
// hand-written, declared-in-the-manifest rule testdata/README.md enforces
// for every other fixture, and would bloat the repo for no reason a
// generated-on-demand file doesn't already serve.
//
// IngestFile uses bufio.Scanner (see maxLineSize's own comment above
// IngestFile), which reads one line at a time rather than the whole file -
// this test is what turns "the code happens to use a streaming API" into a
// property that fails loudly if a future change replaces it with
// os.ReadFile or otherwise slurps the file whole.
func TestIngestFileStreamsRatherThanBuffering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge-session.jsonl")

	const (
		lineCount = 600_000
		// A real, if repetitive, user turn - large enough in aggregate
		// (tens of megabytes) to make "the whole file ended up in memory"
		// obviously distinguishable from "one line at a time did."
		line = `{"type":"user","sessionId":"s1","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"ordinary message text, repeated to build a large synthetic corpus"}}` + "\n"
	)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for range lineCount {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fileSize := fi.Size()
	if fileSize < 50*1024*1024 {
		t.Fatalf("fixture is only %d bytes, too small to distinguish streaming from buffering - this test would pass vacuously", fileSize)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	if _, err := IngestFile(path); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// HeapAlloc can wobble either direction across a GC, so measure growth
	// against HeapSys (the high-water mark the runtime claimed from the OS)
	// rather than a raw before/after delta, which a GC pause could make
	// read as negative.
	const ceiling = 20 * 1024 * 1024
	grew := int64(after.HeapSys) - int64(before.HeapSys)
	if grew > ceiling {
		t.Errorf("heap grew by %s ingesting a %s file - want well under the %s ceiling, proving this buffered the file instead of streaming it",
			formatBytesForTest(grew), formatBytesForTest(fileSize), formatBytesForTest(ceiling))
	}
}

func formatBytesForTest(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
}
