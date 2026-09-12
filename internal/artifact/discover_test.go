package artifact

import (
	"os"
	"path/filepath"
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

func findByPath(t *testing.T, got []Artifact, path string) Artifact {
	t.Helper()
	for _, a := range got {
		if a.Path == path {
			return a
		}
	}
	t.Fatalf("no artifact with path %s in %+v", path, got)
	return Artifact{}
}

func TestDiscoverSkillFlatFile(t *testing.T) {
	home := t.TempDir()
	skillPath := filepath.Join(home, ".claude", "skills", "example.md")
	writeFile(t, skillPath, "---\nname: example-skill\ndescription: does the thing\n---\n\nBody.\n")

	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(home, ".claude", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d artifacts, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Kind != KindSkill || a.Name != "example-skill" || a.Description != "does the thing" {
		t.Errorf("got %+v", a)
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
		t.Fatalf("got %d artifacts, want 1: %+v", len(got), got)
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
		t.Fatalf("got %d artifacts, want 1: %+v", len(got), got)
	}
	if got[0].Name != "plain" {
		t.Errorf("Name = %q, want filename-derived fallback", got[0].Name)
	}
	if got[0].Description != "Diagnosing a PR blocked by an unsigned commit" {
		t.Errorf("Description = %q, want the first heading", got[0].Description)
	}
}

func TestDiscoverMissingDirIsNotError(t *testing.T) {
	got, err := Discover(Locations{SkillDirs: []string{filepath.Join(t.TempDir(), "does-not-exist")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d artifacts, want 0", len(got))
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
		t.Fatalf("got %d artifacts, want 2: %+v", len(got), got)
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
		t.Fatalf("got %d artifacts, want 2: %+v", len(got), got)
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
		t.Errorf("got %d artifacts for missing settings.json, want 0", len(got))
	}

	invalid := filepath.Join(home, ".claude", "settings-invalid.json")
	writeFile(t, invalid, "not json")
	got, err = Discover(Locations{SettingsFiles: []string{invalid}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d artifacts for invalid settings.json, want 0", len(got))
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
