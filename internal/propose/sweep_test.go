package propose

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/asset"
	"github.com/teerakarna/loom/internal/ledger"
)

const sweepStore = "-tmp-proj"

func sweepHome(t *testing.T) (home, memDir string) {
	t.Helper()
	home = t.TempDir()
	memDir = filepath.Join(home, ".claude", "projects", sweepStore, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, memDir
}

func writeSweepFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func unreachableRow(filename string) (ledger.ProposalRow, map[string]any) {
	ev := map[string]any{"store": sweepStore, "filename": filename}
	return ledger.ProposalRow{ID: 7, Kind: KindUnreachableAsset, Subject: sweepStore + "/" + filename}, ev
}

func TestClassifyUnreachableIsSafeWhenEveryCheckPasses(t *testing.T) {
	home, mem := sweepHome(t)
	writeSweepFile(t, filepath.Join(mem, "note_a.md"), "---\nname: note-a\n---\nbody\n")
	writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "- [other](other.md)\n")

	row, ev := unreachableRow("note_a")
	it := Classify(home, row, ev, SlugIndex(home))
	if it.Tier != TierSafe {
		t.Fatalf("tier = %s (%s), want safe", it.Tier, it.Why)
	}
	if it.Line != "- [note-a](note_a.md)" {
		t.Errorf("line = %q", it.Line)
	}
}

// The sibling of the test above: each failed check must keep the item out of
// the safe group, not just the happy path getting in.
func TestClassifyUnreachableIsNotSafeWhenAnyCheckFails(t *testing.T) {
	cases := map[string]func(t *testing.T, mem string) string{
		"no MEMORY.md": func(t *testing.T, mem string) string {
			writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
			return "n"
		},
		"already linked": func(t *testing.T, mem string) string {
			writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
			writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "- [n](n.md)\n")
			return "n"
		},
		"file gone": func(t *testing.T, mem string) string {
			writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")
			return "n"
		},
		"unsafe filename": func(t *testing.T, mem string) string {
			name := "a'b"
			writeSweepFile(t, filepath.Join(mem, name+".md"), "---\nname: n\n---\n")
			writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")
			return name
		},
		"symlinked file": func(t *testing.T, mem string) string {
			target := filepath.Join(t.TempDir(), "real.md")
			writeSweepFile(t, target, "---\nname: n\n---\n")
			if err := os.Symlink(target, filepath.Join(mem, "n.md")); err != nil {
				t.Fatal(err)
			}
			writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")
			return "n"
		},
		"symlinked index": func(t *testing.T, mem string) string {
			writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
			target := filepath.Join(t.TempDir(), "elsewhere.md")
			writeSweepFile(t, target, "x\n")
			if err := os.Symlink(target, filepath.Join(mem, "MEMORY.md")); err != nil {
				t.Fatal(err)
			}
			return "n"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home, mem := sweepHome(t)
			filename := setup(t, mem)
			row, ev := unreachableRow(filename)
			it := Classify(home, row, ev, nil)
			if it.Tier == TierSafe {
				t.Fatalf("classified safe, want decide")
			}
			if it.Why == "" || it.BottomLine == "" || len(it.Options) == 0 {
				t.Errorf("a decide item needs a reason, a bottom line and options: %+v", it)
			}
			if it.Script != "" {
				t.Errorf("a decide item must carry no script: %q", it.Script)
			}
		})
	}
}

func TestClassifyNeverSweepsOtherKinds(t *testing.T) {
	home, _ := sweepHome(t)
	for _, kind := range []string{
		KindFilenameSlugDrift, KindBrokenLink, KindPromoteMemoryDuplicate,
		KindRetireAsset, KindPinModel, KindRevertPolicy, "a_kind_added_later",
	} {
		it := Classify(home, ledger.ProposalRow{ID: 1, Kind: kind}, map[string]any{"store": sweepStore, "filename": "n"}, nil)
		if it.Tier != TierDecide || it.Script != "" {
			t.Errorf("%s: tier %s, script %q - only unreachable_asset may be swept", kind, it.Tier, it.Script)
		}
		if it.BottomLine == "" || len(it.Options) == 0 {
			t.Errorf("%s: no bottom line or options", kind)
		}
	}
}

func TestFilenameDriftAdviceSeparatesStyleFromRealDrift(t *testing.T) {
	row := ledger.ProposalRow{Kind: KindFilenameSlugDrift}
	style, _ := adviceFor(row, map[string]any{"filename": "user_profile", "slug": "user-profile"})
	genuine, _ := adviceFor(row, map[string]any{"filename": "feedback_x", "slug": "something-else"})
	if style == genuine {
		t.Fatalf("a separator-only difference and a real one got the same advice: %q", style)
	}
	if !strings.Contains(style, "separator") {
		t.Errorf("style advice should say so: %q", style)
	}
}

// runScript executes the generated script, which is the only honest test of
// it: the point is what it does to the file, not how it reads.
func runScript(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
}

func TestSweepScriptLinksTheFileAndKeepsEveryExistingByte(t *testing.T) {
	for name, existing := range map[string]string{
		"trailing newline":    "# Index\n- [other](other.md)\n",
		"no trailing newline": "# Index\n- [other](other.md)",
	} {
		t.Run(name, func(t *testing.T) {
			home, mem := sweepHome(t)
			writeSweepFile(t, filepath.Join(mem, "note_a.md"), "---\nname: note-a\n---\n")
			writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), existing)

			row, ev := unreachableRow("note_a")
			script := SweepScript([]SweepItem{Classify(home, row, ev, nil)})
			runScript(t, script)

			got, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md"))
			if !strings.HasPrefix(string(got), existing) {
				t.Errorf("existing content was not preserved:\n%q", got)
			}
			if !asset.MemoryIndex(home, sweepStore)["note_a"] {
				t.Errorf("file is still unreachable after the sweep:\n%q", got)
			}

			// Running it again must change nothing.
			runScript(t, script)
			again, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md"))
			if string(again) != string(got) {
				t.Errorf("second run changed the index:\n%q\nvs\n%q", got, again)
			}
		})
	}
}

func TestSweepScriptIsEmptyWithNothingSafe(t *testing.T) {
	if s := SweepScript([]SweepItem{{Tier: TierDecide, Script: "rm -rf /"}}); s != "" {
		t.Fatalf("a decide item leaked into the script: %q", s)
	}
}

func TestShellQuoteSurvivesAQuote(t *testing.T) {
	out, err := exec.Command("sh", "-c", "printf %s "+shellQuote("a'b c")).Output()
	if err != nil || string(out) != "a'b c" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestClassifyRejectsStoreNamesThatEscapeTheStores(t *testing.T) {
	home, mem := sweepHome(t)
	// A real index and file one level above the stores, where ".." would land.
	up := filepath.Join(home, ".claude", "memory")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweepFile(t, filepath.Join(up, "n.md"), "---\nname: n\n---\n")
	writeSweepFile(t, filepath.Join(up, "MEMORY.md"), "x\n")
	writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
	writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")

	for _, store := range []string{"..", ".", "a/b", "../x", "/", ""} {
		row := ledger.ProposalRow{ID: 1, Kind: KindUnreachableAsset}
		it := Classify(home, row, map[string]any{"store": store, "filename": "n"}, nil)
		if it.Tier == TierSafe {
			t.Errorf("store %q classified safe", store)
		}
	}
}

func TestClassifyRejectsASymlinkedStoreDir(t *testing.T) {
	home, _ := sweepHome(t)
	realDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(filepath.Join(realDir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweepFile(t, filepath.Join(realDir, "memory", "n.md"), "---\nname: n\n---\n")
	writeSweepFile(t, filepath.Join(realDir, "memory", "MEMORY.md"), "x\n")
	if err := os.Symlink(realDir, filepath.Join(home, ".claude", "projects", "-tmp-linkedstore")); err != nil {
		t.Fatal(err)
	}
	it := Classify(home, ledger.ProposalRow{ID: 1, Kind: KindUnreachableAsset},
		map[string]any{"store": "-tmp-linkedstore", "filename": "n"}, nil)
	if it.Tier == TierSafe {
		t.Fatal("a symlinked store directory was classified safe")
	}
}

func TestClassifyFallsBackToFilenameForAnUnsafeSlug(t *testing.T) {
	home, mem := sweepHome(t)
	writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: \"bad name'\"\n---\n")
	writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")
	row, ev := unreachableRow("n")
	slugs := SlugIndex(home)
	if slugs[sweepStore+"/n"] == "" {
		t.Fatal("fixture slug was not read, so this test would pass vacuously")
	}
	if it := Classify(home, row, ev, slugs); it.Line != "- [n](n.md)" {
		t.Errorf("unsafe slug leaked into the line: %q", it.Line)
	}
}

func TestClassifyRejectsASymlinkedMemoryDirectory(t *testing.T) {
	home, _ := sweepHome(t)
	outside := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweepFile(t, filepath.Join(outside, "n.md"), "---\nname: n\n---\n")
	writeSweepFile(t, filepath.Join(outside, "MEMORY.md"), "x\n")
	linked := filepath.Join(home, ".claude", "projects", "-tmp-linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(linked, "memory")); err != nil {
		t.Fatal(err)
	}
	it := Classify(home, ledger.ProposalRow{ID: 1, Kind: KindUnreachableAsset},
		map[string]any{"store": "-tmp-linked", "filename": "n"}, nil)
	if it.Tier == TierSafe {
		t.Fatal("a symlinked memory directory was classified safe")
	}
}

// The script is read and run after Classify's checks, so it must hold up when
// the files changed in between.
func TestSweepScriptSkipsWhenTheFilesChangedAfterItWasGenerated(t *testing.T) {
	gen := func(t *testing.T) (home, mem, script string) {
		home, mem = sweepHome(t)
		writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
		writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "x\n")
		row, ev := unreachableRow("n")
		return home, mem, SweepScript([]SweepItem{Classify(home, row, ev, nil)})
	}

	t.Run("file deleted", func(t *testing.T) {
		_, mem, script := gen(t)
		if err := os.Remove(filepath.Join(mem, "n.md")); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("sh", "-c", script).Run()
		if got, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md")); string(got) != "x\n" {
			t.Errorf("a dangling link was written: %q", got)
		}
	})

	t.Run("index swapped for a symlink", func(t *testing.T) {
		_, mem, script := gen(t)
		outside := filepath.Join(t.TempDir(), "elsewhere.md")
		writeSweepFile(t, outside, "untouched\n")
		if err := os.Remove(filepath.Join(mem, "MEMORY.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(mem, "MEMORY.md")); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("sh", "-c", script).Run()
		if got, _ := os.ReadFile(outside); string(got) != "untouched\n" {
			t.Errorf("the script wrote through a symlink: %q", got)
		}
	})

	t.Run("memory dir swapped for a symlink", func(t *testing.T) {
		_, mem, script := gen(t)
		outside := filepath.Join(t.TempDir(), "memory")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSweepFile(t, filepath.Join(outside, "n.md"), "---\nname: n\n---\n")
		writeSweepFile(t, filepath.Join(outside, "MEMORY.md"), "untouched\n")
		if err := os.RemoveAll(mem); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, mem); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("sh", "-c", script).Run()
		if got, _ := os.ReadFile(filepath.Join(outside, "MEMORY.md")); string(got) != "untouched\n" {
			t.Errorf("the script wrote through a symlinked directory: %q", got)
		}
	})

	t.Run("a skipped item makes the script fail", func(t *testing.T) {
		_, mem, script := gen(t)
		if err := os.Remove(filepath.Join(mem, "n.md")); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("sh", "-c", script).Run(); err == nil {
			t.Error("script exited 0 although it skipped an item")
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		home, mem, script := gen(t)
		runScript(t, script)
		if !asset.MemoryIndex(home, sweepStore)["n"] {
			got, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md"))
			t.Errorf("the unchanged case did not link the file: %q", got)
		}
	})
}

// A parenthesised mention in prose is not a link. The script's guard must
// agree with the classifier on that, or the item is reported swept while the
// file stays unreachable.
func TestSweepScriptGuardAgreesWithTheClassifierOnProse(t *testing.T) {
	home, mem := sweepHome(t)
	writeSweepFile(t, filepath.Join(mem, "n.md"), "---\nname: n\n---\n")
	writeSweepFile(t, filepath.Join(mem, "MEMORY.md"), "see (n.md) for details\n")

	row, ev := unreachableRow("n")
	it := Classify(home, row, ev, nil)
	if it.Tier != TierSafe {
		t.Fatalf("prose is not a link, want safe: %s", it.Why)
	}
	runScript(t, SweepScript([]SweepItem{it}))
	if !asset.MemoryIndex(home, sweepStore)["n"] {
		t.Fatal("the classifier said safe, the script skipped it, and the file is still unreachable")
	}
}
