package asset

import (
	"os"
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

	files, _, err := DiscoverAllMemory(home)
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

	files, _, err := DiscoverAllMemory(home)
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
	files, _, err := DiscoverAllMemory(filepath.Join(t.TempDir(), "does-not-exist"))
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

	files, _, err := DiscoverAllMemory(home)
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

	idx := MemoryIndex(home, "store-a")
	if !idx["a"] || !idx["b-c"] {
		t.Errorf("idx = %+v, want a and b-c present", idx)
	}
	if len(idx) != 2 {
		t.Errorf("idx = %+v, want exactly 2 entries", idx)
	}
}

func TestMemoryIndex_MissingIndexIsEmptyNotError(t *testing.T) {
	home := t.TempDir()
	idx := MemoryIndex(home, "no-such-store")
	if len(idx) != 0 {
		t.Errorf("idx = %+v, want empty", idx)
	}
}

// TestDiscoverAllMemory_OneUnreadableStoreDoesNotFailTheScan is the
// regression test for a bug code review found: an earlier version only
// tolerated os.IsNotExist on a per-store read, so any other error (a
// permission problem, say) on one store's memory directory propagated all
// the way up and failed the whole cross-project scan - taking every other
// store's findings down with it, and beyond that everything list_proposals
// returns, including proposals with nothing to do with memory. Constraint
// 7 (degrade, never block) means one bad store must not do that.
func TestDiscoverAllMemory_OneUnreadableStoreDoesNotFailTheScan(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "good-store", "memory", "a.md"),
		"---\nname: a\n---\nReadable.")

	// A "memory" entry that is a file, not a directory - os.ReadDir on it
	// fails with ENOTDIR, the same shape a permission error takes: a
	// real, non-IsNotExist error on one store's own memory path.
	badStore := filepath.Join(home, ".claude", "projects", "bad-store", "memory")
	writeFile(t, filepath.Join(home, ".claude", "projects", "bad-store", "memory-placeholder"), "")
	if err := os.MkdirAll(filepath.Dir(badStore), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badStore, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, _, err := DiscoverAllMemory(home)
	if err != nil {
		t.Fatalf("DiscoverAllMemory returned an error, want the bad store skipped: %v", err)
	}
	if len(files) != 1 || files[0].Store != "good-store" {
		t.Errorf("got %+v, want the good store's file, unaffected by the bad one", files)
	}
}

// TestDiscoverAllMemory_SubdirectoryConvention is the regression test for a
// bug code review found: an earlier version unconditionally skipped
// directory entries, so a memory asset using the "name/SKILL.md"
// subdirectory convention (a real convention scanMarkdownDir already
// recognizes for this exact kind in the single-project Discover path) was
// invisible to every B7c check - including producing a false broken_link
// report against a [[link]] whose target genuinely existed this way.
func TestDiscoverAllMemory_SubdirectoryConvention(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "deep-topic", "SKILL.md"),
		"---\nname: deep-topic\n---\nBody.")
	// A subdirectory with no SKILL.md is not a memory asset - same rule
	// scanMarkdownDir applies.
	writeFile(t, filepath.Join(home, ".claude", "projects", "store-a", "memory", "not-a-topic", "notes.md"),
		"stray file, not SKILL.md")

	files, _, err := DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %+v, want exactly one file (deep-topic/SKILL.md)", files)
	}
	if files[0].Filename != "deep-topic" {
		t.Errorf("Filename = %q, want %q - the directory's name, not the literal SKILL.md basename", files[0].Filename, "deep-topic")
	}
}
