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
	// recorded), one failed agent (no usage, must NOT appear in the map),
	// one completed background command (no usage, must NOT appear either).
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
		t.Errorf("Kind = %q, want %q (path-based detection needs a /subagents/ segment, see kindForPath)", rs.Kind, "agent")
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
	// "<synthetic>" status line (e.g. a rate-limit notice), that must not
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

// TestIngestFile_UsageCountedOncePerResponse is the regression test for the
// cost bug found by running `loom report` against a real corpus: one API
// response is written to the transcript as several lines, one per content
// block, and every one repeats the same usage object. Summing per line
// overstated real cost by 2.1x across 21 real transcripts. See
// docs/transcript-schema.md, "One response spans several lines".
func TestIngestFile_UsageCountedOncePerResponse(t *testing.T) {
	rs, err := IngestFile("../../testdata/multiline-response.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	// msg_synthetic_aaa spans 4 lines (thinking, text, tool_use, tool_use),
	// each repeating usage of 100/20/1000/40. It must be counted once.
	// msg_synthetic_bbb adds 7/3/500/0, and the trailing line with no
	// message.id at all adds 1/1/0/0 (nothing to deduplicate it against, and
	// dropping it would undercount).
	want := Usage{
		InputTokens:              100 + 7 + 1,
		OutputTokens:             20 + 3 + 1,
		CacheReadInputTokens:     1000 + 500 + 0,
		CacheCreationInputTokens: 40 + 0 + 0,
	}
	if rs.Usage != want {
		t.Errorf("Usage = %+v, want %+v", rs.Usage, want)
	}

	// Tool uses are NOT deduplicated: the two tool_use blocks arrived on two
	// separate lines of the same response and really are two distinct calls.
	if rs.ToolUseCount != 2 {
		t.Errorf("ToolUseCount = %d, want 2 (tool_use blocks must not be deduplicated with usage)", rs.ToolUseCount)
	}
}

func TestApplyEvent_RepeatedMessageIDCountedOnce(t *testing.T) {
	rs := RunSummary{countedMessages: map[string]struct{}{}}
	ev := Event{MessageID: "msg_x", Usage: &Usage{InputTokens: 10}}
	applyEvent(&rs, ev)
	applyEvent(&rs, ev)
	applyEvent(&rs, ev)
	if rs.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10 (same message.id seen 3 times)", rs.Usage.InputTokens)
	}

	// A different id is genuinely new work.
	applyEvent(&rs, Event{MessageID: "msg_y", Usage: &Usage{InputTokens: 5}})
	if rs.Usage.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", rs.Usage.InputTokens)
	}

	// Empty ids are never deduplicated against each other.
	applyEvent(&rs, Event{Usage: &Usage{InputTokens: 2}})
	applyEvent(&rs, Event{Usage: &Usage{InputTokens: 2}})
	if rs.Usage.InputTokens != 19 {
		t.Errorf("InputTokens = %d, want 19 (two id-less lines must both count)", rs.Usage.InputTokens)
	}
}

// TestIngestFile_ToolUsageByBytes is B7b: tool_result byte counts, resolved
// to a tool name via the matching tool_use's id. Covers both content shapes
// (a plain string and a list of blocks - see docs/transcript-schema.md) and
// an orphaned tool_result whose tool_use never appeared in this file.
func TestIngestFile_ToolUsageByBytes(t *testing.T) {
	rs, err := IngestFile("../../testdata/synthetic-occupancy.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string][]ToolUsageEvent{}
	for _, e := range rs.ToolUsage {
		byName[e.ToolName] = append(byName[e.ToolName], e)
	}

	wantReadBytes := int64(len("NOT-A-REAL-SECRET-abcdefgh12345678"))
	if got := byName["Read"]; len(got) != 1 || got[0].ResultBytes != wantReadBytes {
		t.Errorf("Read events = %+v, want one event with ResultBytes %d", got, wantReadBytes)
	} else if got[0].ToolUseID == "" {
		t.Error("ToolUseID must be populated - it's the dedup key a resumed session needs")
	}

	// List-shaped content is measured as its marshaled JSON, not the text
	// alone - see toolResultBytes.
	wantMCPBytes := int64(len(`[{"text":"twelve chars","type":"text"}]`))
	if got := byName["mcp__synth__search"]; len(got) != 1 || got[0].ResultBytes != wantMCPBytes {
		t.Errorf("mcp__synth__search events = %+v, want one event with ResultBytes %d", got, wantMCPBytes)
	}

	// A tool_result whose tool_use never appeared in this file (the fixture's
	// "synthtool-unseen") is attributed to (unknown), not dropped.
	if got := byName[UnknownTool]; len(got) != 1 || got[0].ResultBytes != int64(len("orphaned result")) {
		t.Errorf("(unknown) events = %+v, want one event with ResultBytes %d", got, len("orphaned result"))
	}
}

// TestIngestFile_AssetUsageOnlyFromStructuredInput is B7a, issue #39: a
// Skill tool_use's "skill" input and a Read/Edit/Write tool_use's
// "file_path" input are the only two signals counted, and the fixture
// deliberately repeats both the skill name and the file path as plain text
// in a Bash command and its tool_result - the exact shape that inflated an
// earlier attempt to a near-identical count for every asset. Neither
// decoy may move these counts.
func TestIngestFile_AssetUsageOnlyFromStructuredInput(t *testing.T) {
	rs, err := IngestFile("../../testdata/synthetic-asset-usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if len(rs.SkillTouches) != 1 || rs.SkillTouches[0].Signal != "example-skill" {
		t.Errorf("SkillTouches = %+v, want exactly one touch on example-skill - the Bash decoy must not add one", rs.SkillTouches)
	} else if rs.SkillTouches[0].ToolUseID == "" {
		t.Error("ToolUseID must be populated")
	}

	const path = "/workspace/.claude/plans/my-plan.md"
	// Read once, Edit once, both on the same path: 2 total. The Bash
	// command's own file_path-shaped text (a shell string, not a tool_use
	// input field) must not add a third.
	if len(rs.FileTouches) != 2 || rs.FileTouches[0].Signal != path || rs.FileTouches[1].Signal != path {
		t.Errorf("FileTouches = %+v, want exactly two touches on %s (one Read, one Edit)", rs.FileTouches, path)
	} else if rs.FileTouches[0].ToolUseID == rs.FileTouches[1].ToolUseID {
		t.Error("Read and Edit are different tool_use blocks and must carry different ids")
	}
}

// TestIngestFile_CompactionReadNotGuessed is B7b: a compact_boundary record
// is parsed directly off the host's own compactMetadata, never inferred from
// a cache_read drop (that inference was tried and was wrong 42 times out of
// 42 - docs/design.md, B7b).
func TestIngestFile_CompactionReadNotGuessed(t *testing.T) {
	rs, err := IngestFile("../../testdata/synthetic-occupancy.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if len(rs.Compactions) != 1 {
		t.Fatalf("Compactions = %+v, want exactly 1", rs.Compactions)
	}
	c := rs.Compactions[0]
	want := CompactionEvent{
		UUID: "synth-boundary-0001", Timestamp: c.Timestamp, // set below
		Trigger: "manual", PreTokens: 1000, PostTokens: 100,
		CumulativeDroppedTokens: 900, DurationMs: 5000,
	}
	want.Timestamp = c.Timestamp // timestamp compared separately, not zero-valued
	if c != want {
		t.Errorf("Compactions[0] = %+v, want %+v", c, want)
	}
	if c.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want the line's own timestamp")
	}
}
