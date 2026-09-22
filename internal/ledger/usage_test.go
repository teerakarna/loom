package ledger

import (
	"testing"
	"time"
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

func TestArtifactLookup_ResolveDropsUnmatchedSignals(t *testing.T) {
	l := ArtifactLookup{
		Paths:      map[string]bool{"/s/known.md": true},
		SkillNames: map[string]string{"known-skill": "/s/known.md"},
	}

	got := l.Resolve(
		map[string]int{"known-skill": 3, "renamed-or-deleted-skill": 5},
		map[string]int{"/s/known.md": 2, "/some/unrelated/file.go": 100},
	)
	want := map[string]int{"/s/known.md": 5} // 3 (skill) + 2 (file), both resolve to the same path
	if len(got) != 1 || got["/s/known.md"] != 5 {
		t.Errorf("Resolve = %+v, want %+v", got, want)
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

	if err := db.ReplaceArtifactUsage(id, map[string]int{"/s/a.md": 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceArtifactUsage(id, map[string]int{"/s/a.md": 4, "/s/b.md": 1}); err != nil {
		t.Fatal(err)
	}

	summary, err := db.UsageSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 2 {
		t.Fatalf("UsageSummary = %+v, want exactly 2 entries (the second call's set)", summary)
	}
	if summary["/s/a.md"].Uses != 4 {
		t.Errorf("/s/a.md uses = %d, want 4", summary["/s/a.md"].Uses)
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
	if err := db.ReplaceArtifactUsage(earlyID, map[string]int{"/s/a.md": 1}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertRun(RunRecord{Path: "late.jsonl", Kind: "session", StartedAt: later}); err != nil {
		t.Fatal(err)
	}
	lateID, _ := db.RunIDByPath("late.jsonl")
	if err := db.ReplaceArtifactUsage(lateID, map[string]int{"/s/a.md": 1}); err != nil {
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
