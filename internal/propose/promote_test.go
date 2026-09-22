package propose

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teerakarna/loom/internal/artifact"
)

// writeMemoryFile mirrors internal/artifact's own unexported writeFile
// helper - kept local since it's a one-liner and propose has no reason to
// depend on artifact's test-only code.
func writeMemoryFile(t *testing.T, home, store, filename, content string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "projects", store, "memory", filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMemoryDuplicates_ThreeStoresOnly(t *testing.T) {
	home := t.TempDir()
	// Two stores: not enough. Design doc's own threshold is three or more.
	writeMemoryFile(t, home, "store-a", "fact.md", "---\nname: shared\n---\nSame everywhere.")
	writeMemoryFile(t, home, "store-b", "fact.md", "---\nname: shared\n---\nSame everywhere.")

	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectMemoryDuplicates(files); len(got) != 0 {
		t.Fatalf("got %+v, want none - two stores is not enough", got)
	}

	// A third store with the identical content crosses the threshold.
	writeMemoryFile(t, home, "store-c", "fact.md", "---\nname: shared\n---\nSame everywhere.")
	files, err = artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectMemoryDuplicates(files)
	if len(got) != 1 || got[0].Kind != KindPromoteMemoryDuplicate || got[0].SampleSize != 3 {
		t.Fatalf("got %+v, want one duplicate proposal spanning 3 stores", got)
	}
}

func TestDetectBrokenLinks_ResolvesWithinStoreOnly(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[b]].")
	writeMemoryFile(t, home, "store-a", "b.md", "---\nname: b\n---\nTarget exists.")
	// A link that only resolves in a DIFFERENT store must still count as
	// broken - links resolve within one store, the same scope memory
	// itself loads in.
	writeMemoryFile(t, home, "store-b", "c.md", "---\nname: c\n---\nSee [[b]].")

	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(files)
	if len(got) != 1 {
		t.Fatalf("got %+v, want exactly one broken link (store-b's c.md -> [[b]])", got)
	}
	if got[0].Evidence["store"] != "store-b" || got[0].Evidence["target_slug"] != "b" {
		t.Errorf("got %+v, want store-b -> b", got[0].Evidence)
	}
}

func TestDetectFilenameSlugDrift(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "old_name.md", "---\nname: new-name\n---\nDrifted.")
	writeMemoryFile(t, home, "store-a", "matches.md", "---\nname: matches\n---\nNot drifted.")
	writeMemoryFile(t, home, "store-a", "no_frontmatter.md", "# No frontmatter at all\n")

	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectFilenameSlugDrift(files)
	if len(got) != 1 || got[0].Evidence["filename"] != "old_name" || got[0].Evidence["slug"] != "new-name" {
		t.Fatalf("got %+v, want exactly one drift finding for old_name -> new-name", got)
	}
}

func TestDetectUnreachableArtifacts(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "linked.md", "---\nname: linked\n---\nIn the index.")
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNot in the index.")
	writeMemoryFile(t, home, "store-a", "MEMORY.md", "- [Linked](linked.md)")

	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := detectUnreachableArtifacts(home, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Evidence["filename"] != "orphan" {
		t.Fatalf("got %+v, want exactly one unreachable finding for orphan", got)
	}
}

func TestDetectUnreachableArtifacts_NoIndexMeansEveryFileUnreachable(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nNo MEMORY.md in this store at all.")

	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := detectUnreachableArtifacts(home, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want the file unreachable - a missing index reaches nothing", got)
	}
}

func TestGenerateMemoryFindings_TouchesUserFiles(t *testing.T) {
	for _, kind := range []string{
		KindPromoteMemoryDuplicate, KindBrokenLink, KindUnreachableArtifact, KindFilenameSlugDrift,
	} {
		if !TouchesUserFiles(kind) {
			t.Errorf("TouchesUserFiles(%q) = false, want true - all four B7c kinds touch the user's own memory files", kind)
		}
	}
}

func TestGenerateMemoryFindings_MissingHomeIsNotError(t *testing.T) {
	got, err := GenerateMemoryFindings(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}
