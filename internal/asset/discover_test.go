package asset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findByPath(t *testing.T, got []Asset, path string) Asset {
	t.Helper()
	for _, a := range got {
		if a.Path == path {
			return a
		}
	}
	t.Fatalf("no asset with path %s in %+v", path, got)
	return Asset{}
}

// TestDiscoverSkillFlatFileIsAReferenceNotASkill is the regression test for
// #42: Claude Code only ever loads a skill from <name>/SKILL.md. A flat
// "name.md" sitting directly in a skills directory - frontmatter and all -
// is real and often relied on (a hand-written index, a reference doc) but
// is never loaded as a skill, so it must be discovered as KindReference,
// not miscounted as KindSkill.
func TestDiscoverSkillFlatFileIsAReferenceNotASkill(t *testing.T) {
	home := t.TempDir()
	skillPath := filepath.Join(home, ".claude", "skills", "example.md")
	writeFile(t, skillPath, "---\nname: example-skill\ndescription: does the thing\n---\n\nBody.\n")

	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(home, ".claude", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Kind != KindReference || a.Name != "example-skill" || a.Description != "does the thing" {
		t.Errorf("got %+v, want Kind=%q (name/description still read from frontmatter)", a, KindReference)
	}
}

// TestDiscoverSkillDirMixOfBothShapes is the shape #42 was filed against: a
// skills directory holding one real, loadable skill next to several flat
// reference files (an index, reference docs a skill points readers at).
// Exactly one must come back as KindSkill.
func TestDiscoverSkillDirMixOfBothShapes(t *testing.T) {
	home := t.TempDir()
	skillsDir := filepath.Join(home, ".claude", "skills")
	writeFile(t, filepath.Join(skillsDir, "real-skill", "SKILL.md"), "---\nname: real-skill\n---\nBody.\n")
	writeFile(t, filepath.Join(skillsDir, "SKILLS.md"), "# Index\n\nLinks to the reference docs below.\n")
	writeFile(t, filepath.Join(skillsDir, "some-topic.md"), "# Some Topic\n\nReference material.\n")

	got, err := Discover(Locations{SkillDirs: []string{skillsDir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d assets, want 3: %+v", len(got), got)
	}
	var skills, refs int
	for _, a := range got {
		switch a.Kind {
		case KindSkill:
			skills++
		case KindReference:
			refs++
		default:
			t.Errorf("unexpected kind %q on %+v", a.Kind, a)
		}
	}
	if skills != 1 || refs != 2 {
		t.Errorf("got %d skill(s), %d reference(s), want 1 and 2: %+v", skills, refs, got)
	}
}

func TestDiscoverSkillDirectoryConvention(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "skills", "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\ndescription: dir-based\n---\nBody.\n")
	// A stray non-SKILL.md file in the same skills dir with no matching
	// convention must be ignored, not mistaken for a skill.
	writeFile(t, filepath.Join(home, ".claude", "skills", "notes", "readme.txt"), "not a skill")

	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(home, ".claude", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1: %+v", len(got), got)
	}
	if got[0].Name != "my-skill" || got[0].Description != "dir-based" {
		t.Errorf("got %+v", got[0])
	}
}

func TestDiscoverSkillNoFrontmatterFallsBackToHeading(t *testing.T) {
	home := t.TempDir()
	skillPath := filepath.Join(home, ".claude", "skills", "plain.md")
	writeFile(t, skillPath, "# Diagnosing a PR blocked by an unsigned commit\n\nBody text here.\n")

	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(home, ".claude", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1: %+v", len(got), got)
	}
	if got[0].Name != "plain" {
		t.Errorf("Name = %q, want filename-derived fallback", got[0].Name)
	}
	if got[0].Description != "Diagnosing a PR blocked by an unsigned commit" {
		t.Errorf("Description = %q, want the first heading", got[0].Description)
	}
}

func TestDiscoverTruncatesLongDescription(t *testing.T) {
	home := t.TempDir()
	long := strings.Repeat("x", maxDescriptionRunes+50)
	writeFile(t, filepath.Join(home, ".claude", "skills", "long.md"), "---\ndescription: "+long+"\n---\n")

	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(home, ".claude", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1", len(got))
	}
	d := []rune(got[0].Description)
	if len(d) != maxDescriptionRunes {
		t.Errorf("Description length = %d, want %d (capped)", len(d), maxDescriptionRunes)
	}
	if d[len(d)-1] != '…' {
		t.Errorf("Description doesn't end with an ellipsis: %q", got[0].Description)
	}
}

func TestTruncateMultibyteSafe(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each in UTF-8, must not be split mid-rune
	got := truncate(s, 5)
	if got != strings.Repeat("é", 4)+"…" {
		t.Errorf("truncate(%q, 5) = %q", s, got)
	}
}

func TestDiscoverMissingDirIsNotError(t *testing.T) {
	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(t.TempDir(), "does-not-exist")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d assets, want 0", len(got))
	}
}

func TestDiscoverPlansAndAgents(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "plans", "some-plan.md"), "# A plan\n")
	writeFile(t, filepath.Join(home, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: reviews code\n---\n")

	got, err := Discover(Locations{
		PlanDirs:  []string{filepath.Join(home, ".claude", "plans")},
		AgentDirs: []string{filepath.Join(home, ".claude", "agents")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d assets, want 2: %+v", len(got), got)
	}
	plan := findByPath(t, got, filepath.Join(home, ".claude", "plans", "some-plan.md"))
	if plan.Kind != KindPlan || plan.Name != "some-plan" {
		t.Errorf("plan = %+v", plan)
	}
	agent := findByPath(t, got, filepath.Join(home, ".claude", "agents", "reviewer.md"))
	if agent.Kind != KindAgent || agent.Name != "reviewer" || agent.Description != "reviews code" {
		t.Errorf("agent = %+v", agent)
	}
}

func TestDiscoverHooks(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, settings, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo one"}]}
			],
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "echo two"}]}
			]
		}
	}`)

	got, err := Discover(Locations{SettingsFiles: []string{settings}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d assets, want 2: %+v", len(got), got)
	}
	for _, a := range got {
		if a.Kind != KindHook {
			t.Errorf("got kind %s, want %s", a.Kind, KindHook)
		}
	}
}

func TestDiscoverHooksMissingOrInvalidSettingsIsNotError(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, ".claude", "settings.json")
	got, err := Discover(Locations{SettingsFiles: []string{missing}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d assets for missing settings.json, want 0", len(got))
	}

	invalid := filepath.Join(home, ".claude", "settings-invalid.json")
	writeFile(t, invalid, "not json")
	got, err = Discover(Locations{SettingsFiles: []string{invalid}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d assets for invalid settings.json, want 0", len(got))
	}
}

func TestProjectSlugMatchesObservedConvention(t *testing.T) {
	got := projectSlug("/Users/bertie/projects/personal/dotfiles")
	want := "-Users-bertie-projects-personal-dotfiles"
	if got != want {
		t.Errorf("projectSlug() = %q, want %q", got, want)
	}
}

func TestDefaultLocationsUsesProjectSlugForMemory(t *testing.T) {
	locs := DefaultLocations("/home/u", "/home/u/proj")
	want := filepath.Join("/home/u", ".claude", "projects", "-home-u-proj", "memory")
	if len(locs.MemoryDirs) != 1 || locs.MemoryDirs[0] != want {
		t.Errorf("MemoryDirs = %v, want [%s]", locs.MemoryDirs, want)
	}
}
