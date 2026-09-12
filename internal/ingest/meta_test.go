package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAgentMeta(t *testing.T) {
	meta, ok := ReadAgentMeta("../../testdata/subagents/agent-synthmeta01.jsonl")
	if !ok {
		t.Fatal("expected the companion to be found")
	}
	if meta.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", meta.AgentType)
	}
	if meta.SpawnDepth != 1 {
		t.Errorf("SpawnDepth = %d, want 1", meta.SpawnDepth)
	}
}

func TestReadAgentMetaMissingIsNotAnError(t *testing.T) {
	if _, ok := ReadAgentMeta("../../testdata/subagents/agent-synthnometa.jsonl"); ok {
		t.Error("expected ok=false when there is no companion file")
	}
	if _, ok := ReadAgentMeta("../../testdata/synthetic-session.jsonl"); ok {
		t.Error("expected ok=false for a session transcript")
	}
	if _, ok := ReadAgentMeta("not-a-transcript.txt"); ok {
		t.Error("expected ok=false for a non-.jsonl path")
	}
}

func TestReadAgentMetaToleratesBadContent(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "agent-x.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Not JSON at all.
	if err := os.WriteFile(filepath.Join(dir, "agent-x.meta.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadAgentMeta(transcript); ok {
		t.Error("expected ok=false for invalid JSON, not a panic or an error")
	}

	// Valid JSON, but no agentType to key on.
	if err := os.WriteFile(filepath.Join(dir, "agent-x.meta.json"), []byte(`{"spawnDepth":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadAgentMeta(transcript); ok {
		t.Error("expected ok=false when agentType is absent")
	}
}

func TestIngestFileCapturesAgentTypeAndEffort(t *testing.T) {
	rs, err := IngestFile("../../testdata/subagents/agent-synthmeta01.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Kind != "agent" {
		t.Errorf("Kind = %q, want agent", rs.Kind)
	}
	if rs.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", rs.AgentType)
	}
	if rs.Effort != "high" {
		t.Errorf("Effort = %q, want high", rs.Effort)
	}
}

func TestIngestFileWithoutMetaLeavesAgentTypeEmpty(t *testing.T) {
	rs, err := IngestFile("../../testdata/subagents/agent-synthnometa.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if rs.AgentType != "" {
		t.Errorf("AgentType = %q, want empty when there is no companion", rs.AgentType)
	}
	// The run is still ingested and still has real cost.
	if rs.WeightedCost == 0 {
		t.Error("a run with no companion must still be costed")
	}
	if rs.Effort != "" {
		t.Errorf("Effort = %q, want empty when no line carried one", rs.Effort)
	}
}
