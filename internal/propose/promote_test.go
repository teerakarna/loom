package propose

import (
	"os"
	"path/filepath"
	"strings"
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

func TestEvidenceStores(t *testing.T) {
	cases := []struct {
		name     string
		evidence string
		want     []string
	}{
		{"singular store", `{"store":"store-a","filename":"x"}`, []string{"store-a"}},
		{"plural stores, duplicate-kind evidence", `{"stores":["store-a","store-b","store-c"]}`, []string{"store-a", "store-b", "store-c"}},
		{"malformed json", `not json`, nil},
		{"neither field present", `{"other":"field"}`, nil},
	}
	for _, c := range cases {
		got := EvidenceStores(c.evidence)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestDetectMemoryDuplicates_ThreeStoresOnly(t *testing.T) {
	home := t.TempDir()
	// Two stores: not enough. Design doc's own threshold is three or more.
	writeMemoryFile(t, home, "store-a", "fact.md", "---\nname: shared\n---\nSame everywhere.")
	writeMemoryFile(t, home, "store-b", "fact.md", "---\nname: shared\n---\nSame everywhere.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectMemoryDuplicates(files); len(got) != 0 {
		t.Fatalf("got %+v, want none - two stores is not enough", got)
	}

	// A third store with the identical content crosses the threshold.
	writeMemoryFile(t, home, "store-c", "fact.md", "---\nname: shared\n---\nSame everywhere.")
	files, _, err = asset.DiscoverAllMemory(home)
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

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(home, files)
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
// set being checked. Only when the index genuinely exists, though - a
// straggler code-review finding on the first version of this fix caught
// that the special case treated [[MEMORY]] as always resolved, even in a
// store with no MEMORY.md at all, which is still a real broken link.
func TestDetectBrokenLinks_MemoryIndexNeverReadsAsBroken(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[MEMORY]] for the full index.")
	writeMemoryFile(t, home, "store-a", "MEMORY.md", "- [A](a.md)")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectBrokenLinks(home, files); len(got) != 0 {
		t.Errorf("got %+v, want no broken-link proposal for [[MEMORY]] - the index really exists here", got)
	}

	// A link to a target that exists, just in a different store, is still
	// reported - the special case is narrow to [[MEMORY]] specifically, not
	// a general "anything unresolvable is fine" (and a target that exists
	// nowhere at all is a permitted forward reference - see issue #67 - so
	// this uses a genuinely cross-store target to keep testing real rot).
	writeMemoryFile(t, home, "store-a", "b.md", "---\nname: b\n---\nSee [[elsewhere]].")
	writeMemoryFile(t, home, "store-c", "elsewhere.md", "---\nname: elsewhere\n---\nLives in a different store.")
	files, _, err = asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(home, files)
	found := false
	for _, p := range got {
		if p.Evidence["target_slug"] == "elsewhere" && p.Evidence["store"] == "store-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("got %+v, want a broken link from store-a to elsewhere (exists only in store-c)", got)
	}
}

// TestDetectBrokenLinks_MemoryIndexIsBrokenWhenIndexMissing is the direct
// regression test for the code-review finding: [[MEMORY]] must still be
// reported as broken when the store it appears in has no MEMORY.md at all
// - the special case resolves a reference to a real index, not any
// reference spelled the conventional way regardless of whether one exists.
// Also checks the message text itself, not just target_slug: a second
// review pass found the first fix reused the cross-store wording ("exists,
// just not here") for this case too, which is false - no MEMORY.md exists
// anywhere in this scenario, not just in the wrong place.
func TestDetectBrokenLinks_MemoryIndexIsBrokenWhenIndexMissing(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[MEMORY]] for the full index.")
	// Deliberately no MEMORY.md written in store-a.

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(home, files)
	if len(got) != 1 || got[0].Evidence["target_slug"] != "MEMORY" {
		t.Fatalf("got %+v, want one broken link to MEMORY - no index exists in this store to resolve it", got)
	}
	if strings.Contains(got[0].Summary, "exists") || strings.Contains(got[0].Rationale, "exists, just not here") {
		t.Errorf("Summary/Rationale = %+v, want wording that does not claim MEMORY.md exists somewhere - it does not exist at all", got[0])
	}
}

// TestDetectBrokenLinks_ForwardReferenceIsNotADefect is the regression test
// for issue #67: a [[link]] whose target doesn't exist anywhere on the
// machine yet is a permitted forward reference under Claude Code's own
// memory convention ("a [[name]] that doesn't match an existing memory yet
// is fine, it marks something worth writing later"), not rot - measured on
// a real corpus, 185 of 214 broken_link findings (86%) were exactly this
// class. A target that exists, just in a different store, is a different
// case and still a real finding - the convention permits writing ahead of a
// memory, not permanently mis-scoping a reference to one that exists.
func TestDetectBrokenLinks_ForwardReferenceIsNotADefect(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[not-written-yet]] for context.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectBrokenLinks(home, files); len(got) != 0 {
		t.Errorf("got %+v, want no broken-link proposal - the target exists nowhere yet, a permitted forward reference", got)
	}

	// The target shows up later, in a different store - now it's a real,
	// actionable cross-store scoping problem, not a forward reference.
	writeMemoryFile(t, home, "store-b", "not-written-yet.md", "---\nname: not-written-yet\n---\nNow it exists, elsewhere.")
	files, _, err = asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(home, files)
	if len(got) != 1 || got[0].Evidence["target_slug"] != "not-written-yet" {
		t.Errorf("got %+v, want one broken link now that the target exists (in the wrong store)", got)
	}
}

func TestDetectFilenameSlugDrift(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "old_name.md", "---\nname: new-name\n---\nDrifted.")
	writeMemoryFile(t, home, "store-a", "matches.md", "---\nname: matches\n---\nNot drifted.")
	writeMemoryFile(t, home, "store-a", "no_frontmatter.md", "# No frontmatter at all\n")

	files, _, err := asset.DiscoverAllMemory(home)
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

	files, _, err := asset.DiscoverAllMemory(home)
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

	files, _, err := asset.DiscoverAllMemory(home)
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
	got, scanned, err := GenerateMemoryFindings(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
	if len(scanned) != 0 {
		t.Errorf("scanned = %+v, want none - a missing home has no stores to have scanned", scanned)
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

	files, _, err := asset.DiscoverAllMemory(home)
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

// TestStoreProtectsPendingProposalsForAStoreNotScannedThisPass is the
// regression test for issue #59, found by code review while shipping #40's
// withdrawal mechanism. DiscoverAllMemory skips any store whose memory
// directory is unreadable this pass, by design (constraint 7) - but before
// this fix, that looked identical to WithdrawStalePending as the store's
// findings having genuinely stopped being true, so every real, unchanged
// proposal for that store got marked withdrawn on the one pass it couldn't
// be read.
func TestStoreProtectsPendingProposalsForAStoreNotScannedThisPass(t *testing.T) {
	db := openDB(t)
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNo MEMORY.md links this.")

	// First pass: store-a scans fine, the unreachable-asset finding raises.
	findings, scanned, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if !scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a present - its memory directory is readable this pass", scanned)
	}
	if _, err := Store(db, findings, scanned, now); err != nil {
		t.Fatal(err)
	}
	pending, err := db.ListProposals(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Kind != KindUnreachableAsset {
		t.Fatalf("got %+v, want exactly one unreachable_asset proposal after the first pass", pending)
	}

	// Second pass: store-a's memory directory becomes unreadable - not
	// gone, not fixed, transiently unreadable this one pass (same
	// technique asset's own constraint-7 regression test uses: a file
	// where a directory should be).
	memDir := filepath.Join(home, ".claude", "projects", "store-a", "memory")
	if err := os.RemoveAll(memDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, scanned, err = GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("got %+v, want no findings - store-a could not be read this pass", findings)
	}
	if scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a absent this pass", scanned)
	}
	if _, err := Store(db, findings, scanned, now); err != nil {
		t.Fatal(err)
	}

	pending, err = db.ListProposals(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("got %+v, want the unreachable_asset proposal still pending - store-a was never actually "+
			"rescanned this pass, so nothing said its finding stopped being true", pending)
	}
}

// TestStoreStillWithdrawsAGenuinelyFixedFinding is the control case
// alongside the regression test above: when a store IS fully scanned and
// its finding really is gone, the proposal must still withdraw exactly as
// #40 intended - the fix for #59 protects an unscanned store, not every
// store indiscriminately.
func TestStoreStillWithdrawsAGenuinelyFixedFinding(t *testing.T) {
	db := openDB(t)
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNo MEMORY.md links this.")

	findings, scanned, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Store(db, findings, scanned, now); err != nil {
		t.Fatal(err)
	}
	if pending, _ := db.ListProposals(true); len(pending) != 1 {
		t.Fatalf("got %+v, want exactly one proposal after the first pass", pending)
	}

	// The fix: add the missing MEMORY.md entry. store-a scans fine and the
	// finding is genuinely gone this time, not merely unreadable.
	writeMemoryFile(t, home, "store-a", "MEMORY.md", "- [Orphan](orphan.md) - now linked")

	findings, scanned, err = GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if !scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a present - it was readable this pass", scanned)
	}
	if len(findings) != 0 {
		t.Fatalf("got %+v, want none - the file is linked now", findings)
	}
	if _, err := Store(db, findings, scanned, now); err != nil {
		t.Fatal(err)
	}

	if pending, _ := db.ListProposals(true); len(pending) != 0 {
		t.Errorf("got %+v, want none - store-a was fully scanned and the finding is genuinely gone", pending)
	}
}
