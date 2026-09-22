package ledger

import (
	"testing"
	"time"

	"github.com/teerakarna/loom/internal/ingest"
)

func TestBuildArtifactLookup(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/s/a.md", Name: "skill-a"}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "plan", Path: "/p/b.md", Name: "plan-b"}, now); err != nil {
		t.Fatal(err)
	}

	l, err := db.BuildArtifactLookup()
	if err != nil {
		t.Fatal(err)
	}
	if !l.Paths["/s/a.md"] || !l.Paths["/p/b.md"] {
		t.Errorf("Paths = %+v, want both artifacts present", l.Paths)
	}
	if l.SkillNames["skill-a"] != "/s/a.md" {
		t.Errorf("SkillNames[skill-a] = %q, want /s/a.md", l.SkillNames["skill-a"])
	}
	// A plan is not a skill and must not appear in SkillNames, even keyed
	// by its own artifact name - the two lookup spaces are for different
	// signal shapes (a skill name vs. a file path) and must not cross.
	if _, ok := l.SkillNames["plan-b"]; ok {
		t.Error("a non-skill artifact must not appear in SkillNames")
	}
}

// TestBuildArtifactLookup_NameCollisionIsDeterministic is the regression
// test for a bug code review found: discover.go scans both home and cwd
// skill dirs, a documented pattern where a project-level skill can share a
// name with a global one, and artifacts.path (not name) is what UpsertArtifact
// keys on - so both persist as separate rows. Without an ORDER BY, which one
// SkillNames[name] ends up pointing at was whatever order SQLite happened to
// return, and could change between two calls with identical data. It must
// not: same data in, same answer out, every time.
func TestBuildArtifactLookup_NameCollisionIsDeterministic(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/project/.claude/skills/deploy.md", Name: "deploy"}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/home/.claude/skills/deploy.md", Name: "deploy"}, now); err != nil {
		t.Fatal(err)
	}

	first, err := db.BuildArtifactLookup()
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := db.BuildArtifactLookup()
		if err != nil {
			t.Fatal(err)
		}
		if again.SkillNames["deploy"] != first.SkillNames["deploy"] {
			t.Fatalf("SkillNames[deploy] changed between calls: %q then %q", first.SkillNames["deploy"], again.SkillNames["deploy"])
		}
	}
}

func TestArtifactLookup_ResolveDropsUnmatchedSignals(t *testing.T) {
	l := ArtifactLookup{
		Paths:      map[string]bool{"/s/known.md": true},
		SkillNames: map[string]string{"known-skill": "/s/known.md"},
	}

	got := l.Resolve(
		[]ingest.ArtifactTouch{
			{ToolUseID: "t1", Signal: "known-skill"},
			{ToolUseID: "t2", Signal: "renamed-or-deleted-skill"},
		},
		[]ingest.ArtifactTouch{
			{ToolUseID: "t3", Signal: "/s/known.md"},
			{ToolUseID: "t4", Signal: "/some/unrelated/file.go"},
		},
	)
	// t1 and t3 resolve (to the same path, via different signal kinds); t2
	// and t4 are dropped, not guessed at.
	want := []ResolvedTouch{
		{ToolUseID: "t1", ArtifactPath: "/s/known.md"},
		{ToolUseID: "t3", ArtifactPath: "/s/known.md"},
	}
	if len(got) != len(want) {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Resolve[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReplaceArtifactUsage_ReplacesNotAccumulates(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertRun(RunRecord{Path: "run-a.jsonl", Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	id, err := db.RunIDByPath("run-a.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.ReplaceArtifactUsage(id, []ResolvedTouch{{ToolUseID: "t1", ArtifactPath: "/s/a.md"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceArtifactUsage(id, []ResolvedTouch{
		{ToolUseID: "t2", ArtifactPath: "/s/a.md"},
		{ToolUseID: "t3", ArtifactPath: "/s/a.md"},
		{ToolUseID: "t4", ArtifactPath: "/s/a.md"},
		{ToolUseID: "t5", ArtifactPath: "/s/a.md"},
		{ToolUseID: "t6", ArtifactPath: "/s/b.md"},
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := db.UsageSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 2 {
		t.Fatalf("UsageSummary = %+v, want exactly 2 entries (the second call's set - t1 must be gone)", summary)
	}
	if summary["/s/a.md"].Uses != 4 {
		t.Errorf("/s/a.md uses = %d, want 4", summary["/s/a.md"].Uses)
	}
}

// TestReplaceArtifactUsage_DedupesAcrossResumedSessions is the regression
// test for the bug code review found: a resumed session replays its prior
// Skill/Read/Edit/Write tool_use blocks verbatim, and an earlier version of
// this table had no per-event identity to dedupe a replay against. Same
// shape as TestReplaceToolUsage_DedupesAcrossResumedSessions.
func TestReplaceArtifactUsage_DedupesAcrossResumedSessions(t *testing.T) {
	db := openTestDB(t)
	original := insertTestRun(t, db, "original-session.jsonl")
	resumed := insertTestRun(t, db, "resumed-session.jsonl")

	shared := ResolvedTouch{ToolUseID: "shared-call", ArtifactPath: "/s/a.md"}
	if err := db.ReplaceArtifactUsage(original, []ResolvedTouch{shared}); err != nil {
		t.Fatal(err)
	}
	newTouch := ResolvedTouch{ToolUseID: "new-after-resume", ArtifactPath: "/s/a.md"}
	if err := db.ReplaceArtifactUsage(resumed, []ResolvedTouch{shared, newTouch}); err != nil {
		t.Fatal(err)
	}

	summary, err := db.UsageSummary()
	if err != nil {
		t.Fatal(err)
	}
	// 2 uses (shared once, new once) - not 3, which is what double-counting
	// the shared touch would produce.
	if summary["/s/a.md"].Uses != 2 {
		t.Errorf("/s/a.md uses = %d, want 2", summary["/s/a.md"].Uses)
	}
}

func TestUsageSummary_LastUsedIsTheMostRecentRun(t *testing.T) {
	db := openTestDB(t)
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := db.InsertRun(RunRecord{Path: "early.jsonl", Kind: "session", StartedAt: earlier}); err != nil {
		t.Fatal(err)
	}
	earlyID, _ := db.RunIDByPath("early.jsonl")
	if err := db.ReplaceArtifactUsage(earlyID, []ResolvedTouch{{ToolUseID: "t1", ArtifactPath: "/s/a.md"}}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertRun(RunRecord{Path: "late.jsonl", Kind: "session", StartedAt: later}); err != nil {
		t.Fatal(err)
	}
	lateID, _ := db.RunIDByPath("late.jsonl")
	if err := db.ReplaceArtifactUsage(lateID, []ResolvedTouch{{ToolUseID: "t2", ArtifactPath: "/s/a.md"}}); err != nil {
		t.Fatal(err)
	}

	summary, err := db.UsageSummary()
	if err != nil {
		t.Fatal(err)
	}
	s := summary["/s/a.md"]
	if s.Uses != 2 {
		t.Errorf("Uses = %d, want 2 (one from each run)", s.Uses)
	}
	if s.LastUsedAt != formatTime(later) {
		t.Errorf("LastUsedAt = %q, want the later run's timestamp %q", s.LastUsedAt, formatTime(later))
	}
}

// TestUsageSummary_AbsentMeansNeverUsed is the contract UsageSummary's own
// doc comment states: a path with no usage rows does not appear at all,
// rather than appearing with a zero-value summary. propose.retireStaleArtifacts
// depends on this to distinguish "never used" from "used, a long time ago".
func TestUsageSummary_AbsentMeansNeverUsed(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/s/untouched.md", Name: "untouched"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	summary, err := db.UsageSummary()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := summary["/s/untouched.md"]; ok {
		t.Error("an artifact with no usage rows must not appear in the summary at all")
	}
}

// TestAgentTypeLastUsed is docs/design.md's B7a claim actually being true:
// runs.agent_type answers "was this agent invoked" with no new signal,
// since B3a already records it. Found not wired into retirement by code
// review - this covers the read side the fix depends on.
func TestAgentTypeLastUsed(t *testing.T) {
	db := openTestDB(t)
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := db.InsertRun(RunRecord{Path: "a1.jsonl", Kind: "agent", AgentType: "Explore", StartedAt: earlier}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{Path: "a2.jsonl", Kind: "agent", AgentType: "Explore", StartedAt: later}); err != nil {
		t.Fatal(err)
	}
	// A session run with no agent_type must not pollute the map with a ""
	// entry.
	if err := db.InsertRun(RunRecord{Path: "s1.jsonl", Kind: "session", StartedAt: later}); err != nil {
		t.Fatal(err)
	}

	got, err := db.AgentTypeLastUsed()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("AgentTypeLastUsed = %+v, want exactly one entry", got)
	}
	if got["Explore"] != formatTime(later) {
		t.Errorf("Explore = %q, want the later run's timestamp %q", got["Explore"], formatTime(later))
	}
	if _, ok := got[""]; ok {
		t.Error("a session run's empty agent_type must not appear as an entry")
	}
}
