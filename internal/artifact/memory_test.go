package artifact

import (
	"path/filepath"
	"testing"
)

func TestDiscoverAllMemory_CrossProject(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "fact.md"),
		"---\nname: shared-fact\n---\nSame content everywhere.")
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-b", "memory", "fact.md"),
		"---\nname: shared-fact\n---\nSame content everywhere.")
	// The index itself must never be treated as a memory file - see
	// memoryIndexName.
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "MEMORY.md"),
		"- [Shared fact](fact.md)")

	files, err := DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2 (MEMORY.md must be excluded): %+v", len(files), files)
	}
	for _, f := range files {
		if f.Filename != "fact" || f.Slug != "shared-fact" {
			t.Errorf("got %+v, want Filename=fact Slug=shared-fact", f)
		}
	}
	if files[0].ContentHash != files[1].ContentHash {
		t.Error("identical content across stores must hash identically")
	}
	if files[0].Store == files[1].Store {
		t.Error("the two files must be attributed to their own distinct stores")
	}
}

func TestDiscoverAllMemory_LinksExtracted(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "a.md"),
		"---\nname: a\n---\nSee [[b]] and [[c]] for more.")

	files, err := DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	want := []string{"b", "c"}
	if len(files[0].Links) != 2 || files[0].Links[0] != want[0] || files[0].Links[1] != want[1] {
		t.Errorf("Links = %+v, want %+v", files[0].Links, want)
	}
}

func TestDiscoverAllMemory_MissingHomeIsNotError(t *testing.T) {
	files, err := DiscoverAllMemory(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if files != nil {
		t.Errorf("got %+v, want nil", files)
	}
}

func TestDiscoverAllMemory_NoFrontmatterLeavesSlugEmpty(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "plain.md"), "# Just a heading\n\nBody.")

	files, err := DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Slug != "" {
		t.Errorf("got %+v, want one file with an empty Slug", files)
	}
}

func TestMemoryIndex(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "MEMORY.md"),
		"# Memory Index\n\n- [A fact](a.md) - hook\n- [Another](b-c.md) - hook\n")

	idx, err := MemoryIndex(home, "store-a")
	if err != nil {
		t.Fatal(err)
	}
	if !idx["a"] || !idx["b-c"] {
		t.Errorf("idx = %+v, want a and b-c present", idx)
	}
	if len(idx) != 2 {
		t.Errorf("idx = %+v, want exactly 2 entries", idx)
	}
}

func TestMemoryIndex_MissingIndexIsEmptyNotError(t *testing.T) {
	home := t.TempDir()
	idx, err := MemoryIndex(home, "no-such-store")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 0 {
		t.Errorf("idx = %+v, want empty", idx)
	}
}
