package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderSkipsDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	out, err := Render(dir, []Decision{
		{AgentType: "Explore", Model: "haiku", Source: SourceDefault, SampleSize: 14},
		{AgentType: "fork", Model: "sonnet", Source: SourceDefault},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Written) != 0 {
		t.Errorf("wrote %v, want nothing: a file restating a default is material with no information", out.Written)
	}
	if len(out.Skipped) != 2 {
		t.Errorf("Skipped = %v, want both explained", out.Skipped)
	}
	// Not even the directory, if there was nothing to put in it.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("expected no directory to be created when nothing is rendered")
	}
}

func TestRenderWritesStoredAndTrustedEvidence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	out, err := Render(dir, []Decision{
		{AgentType: "Explore", Model: "opus", Effort: "high", Source: SourceStored, Rationale: "set deliberately"},
		{AgentType: "Worker", Model: "haiku", Effort: "low", Source: SourceEvidence, SampleSize: MinSampleSize, Rationale: "measured"},
		{AgentType: "Skipped", Model: "sonnet", Source: SourceDefault},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Written) != 2 {
		t.Fatalf("Written = %v, want 2", out.Written)
	}

	body, err := os.ReadFile(filepath.Join(dir, "Explore.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"model: opus", "effort: high", generatedMarker, "Source: stored", "Criteria version:"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered file missing %q:\n%s", want, text)
		}
	}
}

func TestRenderOverwritesItsOwnButRefusesHandWritten(t *testing.T) {
	dir := t.TempDir()
	d := []Decision{{AgentType: "Explore", Model: "opus", Source: SourceStored}}

	// First pass writes it; second overwrites rather than duplicating.
	if _, err := Render(dir, d); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, d); err != nil {
		t.Fatalf("re-rendering its own file should succeed: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("got %d files after two passes, want 1 (overwrite, never accumulate)", len(entries))
	}

	// A file Loom did not write is never clobbered.
	handWritten := filepath.Join(dir, "Explore.md")
	if err := os.WriteFile(handWritten, []byte("---\nname: Explore\n---\nmine, not loom's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, d); err == nil {
		t.Error("expected Render to refuse a file it did not generate")
	}
	body, _ := os.ReadFile(handWritten)
	if !strings.Contains(string(body), "mine, not loom's") {
		t.Error("the hand-written file was modified despite the refusal")
	}
}

func TestRenderRejectsUnsafeAgentTypes(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../escape", "nested/type", "a:b", ""} {
		if _, err := Render(dir, []Decision{{AgentType: bad, Model: "x", Source: SourceStored}}); err == nil {
			t.Errorf("expected an error for agent type %q", bad)
		}
	}
	// Nothing escaped.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); !os.IsNotExist(err) {
		t.Error("a file was written outside the output directory")
	}
}

func TestRenderEnforcesFileLimit(t *testing.T) {
	dir := t.TempDir()
	var many []Decision
	for i := range MaxGeneratedFiles + 1 {
		many = append(many, Decision{AgentType: "agent" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Model: "x", Source: SourceStored})
	}
	if _, err := Render(dir, many); err == nil {
		t.Errorf("expected a refusal past the %d-file limit", MaxGeneratedFiles)
	}
}
