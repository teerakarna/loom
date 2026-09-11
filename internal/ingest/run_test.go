package ingest

import "testing"

func TestIngestFileSession(t *testing.T) {
	rs, err := IngestFile("../../testdata/synthetic-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if rs.Kind != "session" {
		t.Errorf("Kind = %q, want %q", rs.Kind, "session")
	}
	if rs.SessionID != "synth-session-1" {
		t.Errorf("SessionID = %q", rs.SessionID)
	}

	// Two assistant turns: (100,50,20,10) + (200,30,40,0).
	wantUsage := Usage{
		InputTokens:                 300,
		OutputTokens:                80,
		CacheReadInputTokens:        60,
		CacheCreationInputTokens:    10,
		CacheCreationEphemeral1hTok: 10,
	}
	if rs.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", rs.Usage, wantUsage)
	}

	if rs.ToolUseCount != 2 {
		t.Errorf("ToolUseCount = %d, want 2", rs.ToolUseCount)
	}
	if rs.DenialCount != 1 {
		t.Errorf("DenialCount = %d, want 1", rs.DenialCount)
	}
	if rs.FeedbackCount != 1 {
		t.Errorf("FeedbackCount = %d, want 1", rs.FeedbackCount)
	}

	// Three task-notifications in the fixture: one completed agent (usage
	// recorded), one failed agent (no usage — must NOT appear in the map),
	// one completed background command (no usage — must NOT appear either).
	if len(rs.AgentReconciliations) != 1 {
		t.Fatalf("AgentReconciliations = %+v, want exactly 1 entry", rs.AgentReconciliations)
	}
	u, ok := rs.AgentReconciliations["synthagent0001"]
	if !ok {
		t.Fatal("expected a reconciliation entry for synthagent0001")
	}
	if u.SubagentTokens != 777 || u.ToolUses != 3 || u.DurationMs != 5000 {
		t.Errorf("reconciliation usage = %+v, want {777 3 5000}", u)
	}
	if _, ok := rs.AgentReconciliations["synthagent0002"]; ok {
		t.Error("failed agent must not produce a reconciliation entry")
	}
	if _, ok := rs.AgentReconciliations["bsynthback01"]; ok {
		t.Error("background command must not produce a reconciliation entry")
	}

	if rs.WeightedCost != WeightedCost(wantUsage) {
		t.Errorf("WeightedCost = %v, want %v", rs.WeightedCost, WeightedCost(wantUsage))
	}
}

func TestIngestFileAgent(t *testing.T) {
	rs, err := IngestFile("../../testdata/subagents/agent-synthagent0001.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Kind != "agent" {
		t.Errorf("Kind = %q, want %q (path-based detection needs a /subagents/ segment — see kindForPath)", rs.Kind, "agent")
	}

	// (50,25,5,0) + (30,10,5,0).
	wantUsage := Usage{InputTokens: 80, OutputTokens: 35, CacheReadInputTokens: 10}
	if rs.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", rs.Usage, wantUsage)
	}
}

func TestSyntheticModelNeverWinsAttribution(t *testing.T) {
	// Regression test: found by running against real history. A real model
	// does genuine (costly) work, then the run ends with a locally-injected
	// "<synthetic>" status line (e.g. a rate-limit notice) — that must not
	// overwrite the run's model attribution, or its real cost gets mislabeled
	// under a model that did no work and cost nothing.
	rs := RunSummary{}
	applyEvent(&rs, Event{Model: "claude-sonnet-5", Usage: &Usage{InputTokens: 100}})
	applyEvent(&rs, Event{Model: syntheticModel})
	if rs.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q, want %q (synthetic status line must not win attribution)", rs.Model, "claude-sonnet-5")
	}
}

func TestAgentIDFromPath(t *testing.T) {
	cases := []struct {
		path   string
		wantID string
		wantOK bool
	}{
		{"/x/y/subagents/agent-abc123.jsonl", "abc123", true},
		{"/x/y/session.jsonl", "", false},
		{"agent-abc123.jsonl", "abc123", true},
	}
	for _, c := range cases {
		id, ok := AgentIDFromPath(c.path)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("AgentIDFromPath(%q) = (%q, %v), want (%q, %v)", c.path, id, ok, c.wantID, c.wantOK)
		}
	}
}
