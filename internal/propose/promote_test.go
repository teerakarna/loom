package propose

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teerakarna/loom/internal/asset"
)

// writeMemoryFile mirrors internal/asset's own unexported writeFile
// helper - kept local since it's a one-liner and propose has no reason to
// depend on asset's test-only code.
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

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectMemoryDuplicates(files); len(got) != 0 {
		t.Fatalf("got %+v, want none - two stores is not enough", got)
	}

	// A third store with the identical content crosses the threshold.
	writeMemoryFile(t, home, "store-c", "fact.md", "---\nname: shared\n---\nSame everywhere.")
	files, err = asset.DiscoverAllMemory(home)
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

	files, err := asset.DiscoverAllMemory(home)
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

// TestDetectBrokenLinks_MemoryIndexNeverReadsAsBroken is the regression
// test for issue #65's second finding: DiscoverAllMemory deliberately
// excludes MEMORY.md from the file set (it is the index, not a fact being
// indexed), so a real reference to it, "[[MEMORY]] for the full index",
// could never resolve - not because the index is actually missing, but
// because nothing ever registered a slug for a file that was never in the
// set being checked.
func TestDetectBrokenLinks_MemoryIndexNeverReadsAsBroken(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[MEMORY]] for the full index.")

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectBrokenLinks(files); len(got) != 0 {
		t.Errorf("got %+v, want no broken-link proposal for [[MEMORY]] - it has no correct resolution to suggest", got)
	}

	// A link to anything else not on disk is still reported - the special
	// case is narrow, not a general "anything unresolvable is fine".
	writeMemoryFile(t, home, "store-a", "b.md", "---\nname: b\n---\nSee [[nonexistent]].")
	files, err = asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(files)
	if len(got) != 1 || got[0].Evidence["target_slug"] != "nonexistent" {
		t.Errorf("got %+v, want exactly one broken link, to nonexistent", got)
	}
}

func TestDetectFilenameSlugDrift(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "old_name.md", "---\nname: new-name\n---\nDrifted.")
	writeMemoryFile(t, home, "store-a", "matches.md", "---\nname: matches\n---\nNot drifted.")
	writeMemoryFile(t, home, "store-a", "no_frontmatter.md", "# No frontmatter at all\n")

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectFilenameSlugDrift(files)
	if len(got) != 1 || got[0].Evidence["filename"] != "old_name" || got[0].Evidence["slug"] != "new-name" {
		t.Fatalf("got %+v, want exactly one drift finding for old_name -> new-name", got)
	}
}

func TestDetectUnreachableAssets(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "linked.md", "---\nname: linked\n---\nIn the index.")
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNot in the index.")
	writeMemoryFile(t, home, "store-a", "MEMORY.md", "- [Linked](linked.md)")

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectUnreachableAssets(home, files)
	if len(got) != 1 || got[0].Evidence["filename"] != "orphan" {
		t.Fatalf("got %+v, want exactly one unreachable finding for orphan", got)
	}
}

func TestDetectUnreachableAssets_NoIndexMeansEveryFileUnreachable(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nNo MEMORY.md in this store at all.")

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectUnreachableAssets(home, files)
	if len(got) != 1 {
		t.Fatalf("got %+v, want the file unreachable - a missing index reaches nothing", got)
	}
}

func TestGenerateMemoryFindings_TouchesUserFiles(t *testing.T) {
	for _, kind := range []string{
		KindPromoteMemoryDuplicate, KindBrokenLink, KindUnreachableAsset, KindFilenameSlugDrift,
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

// TestDetectMemoryDuplicates_DeterministicOrder is the regression test for
// a bug code review found: an earlier version ranged directly over a Go
// map keyed by content hash, whose iteration order is randomized per call.
// That mattered once the total findings across every B7c check exceeded
// the pending-proposal cap (20): UpsertProposal accepts new subjects in
// the order Store sees them, so two back-to-back runs against identical,
// unchanged disk state could persist a different subset of duplicate
// findings each time - contradicting Generate's own stated contract that
// repeated runs do not reshuffle the list under a reader.
func TestDetectMemoryDuplicates_DeterministicOrder(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		for _, store := range []string{"store-1", "store-2", "store-3"} {
			writeMemoryFile(t, home, store, name+".md",
				"---\nname: "+name+"\n---\nContent for "+name)
		}
	}

	files, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}

	first := detectMemoryDuplicates(files)
	if len(first) != 5 {
		t.Fatalf("got %d duplicate proposals, want 5", len(first))
	}
	for i := 0; i < 20; i++ {
		again := detectMemoryDuplicates(files)
		if len(again) != len(first) {
			t.Fatalf("run %d: got %d proposals, want %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Subject != first[j].Subject {
				t.Fatalf("run %d: order changed at index %d: %q then %q", i, j, first[j].Subject, again[j].Subject)
			}
		}
	}
}
