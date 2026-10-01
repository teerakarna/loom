package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

// buildReadOnlyFixtureTree writes a representative sample of everything
// loom reads - a transcript, a memory file, settings.json, a skill - under
// root, then chmods every directory and file read-only. Returns a snapshot
// (path -> sha256) taken before the chmod, for the caller to compare against
// after running loom's pipeline against it.
//
// Permissions are restored in a t.Cleanup registered before this returns,
// so it runs before t.TempDir's own removal cleanup (cleanups run in LIFO
// order) - a read-only directory cannot have its own entries unlinked, so
// skipping this would leave the temp dir behind, or fail the test run
// outright on an OS that errors loudly on it.
func buildReadOnlyFixtureTree(t *testing.T) (root string, before map[string]string) {
	t.Helper()
	root = t.TempDir()

	projDir := filepath.Join(root, ".claude", "projects", "myproj")
	transcriptWithChildRows(t, filepath.Join(projDir, "session.jsonl"))

	memDir := filepath.Join(projDir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately unreachable from any MEMORY.md - a real, non-vacuous
	// finding for propose.GenerateMemoryFindings to surface, proving the
	// scan actually ran rather than trivially finding nothing.
	if err := os.WriteFile(filepath.Join(memDir, "orphan.md"),
		[]byte("---\nname: orphan\n---\nNo MEMORY.md links this.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	settingsDir := filepath.Join(root, ".claude")
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"),
		[]byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(root, ".claude", "skills", "example-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: example-skill\ndescription: an example skill\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before = hashTree(t, root)

	var dirs, files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
		return nil
	})
	// Files first, then directories - a directory needs its own write bit
	// to chmod the files inside it.
	for _, f := range files {
		if err := os.Chmod(f, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range dirs {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, d := range dirs {
			_ = os.Chmod(d, 0o755)
		}
		for _, f := range files {
			_ = os.Chmod(f, 0o644)
		}
	})

	return root, before
}

// hashTree returns path -> sha256 of every regular file's content under root.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		out[p] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestIngestAndProposeNeverWriteOutsideTheLedger is the regression test for
// docs/design.md's Verification section, "Write containment" (zero writes
// outside the database and generated-output directory, settings files
// never opened for write) and its B7 follow-on "Write containment catches
// the refused verb" (extend the assertion set to the proposal directory).
//
// "The proposal directory" never actually exists as a filesystem target -
// B7c shipped proposals as rows in the ledger's own proposals table
// ("minus the write" of the design doc's own words, docs/design.md, B7c) -
// so there is no separate directory to extend containment to. What this
// test extends is the *scope*: not just ingest, but proposal generation and
// storage too, run against the identical read-only tree, to prove that
// scope never grew a filesystem side-channel of its own.
//
// The read-only permissions (not just an assertion afterward) are the real
// enforcement: any write attempt - loom's own, or a future regression -
// fails loudly as a permission error the moment it happens, rather than
// silently succeeding and only being caught by the before/after comparison
// at the end.
func TestIngestAndProposeNeverWriteOutsideTheLedger(t *testing.T) {
	root, before := buildReadOnlyFixtureTree(t)
	t.Setenv("HOME", root) // propose.GenerateMemoryFindings reads os.UserHomeDir()

	ledgerDir := t.TempDir() // deliberately outside root: the one place writes belong
	db, err := ledger.Open(filepath.Join(ledgerDir, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	projectsRoot := filepath.Join(root, ".claude", "projects")
	if err := ingestAll(db, projectsRoot, projectsRoot, true); err != nil {
		t.Fatalf("ingestAll against a read-only tree failed: %v", err)
	}

	now := time.Now()
	generated, err := propose.Generate(db, now)
	if err != nil {
		t.Fatalf("propose.Generate failed: %v", err)
	}
	memoryFindings, scannedStores, err := propose.GenerateMemoryFindings(root)
	if err != nil {
		t.Fatalf("propose.GenerateMemoryFindings against a read-only tree failed: %v", err)
	}
	generated = append(generated, memoryFindings...)
	if _, err := propose.Store(db, generated, &scannedStores, now); err != nil {
		t.Fatalf("propose.Store failed: %v", err)
	}

	foundOrphan := false
	for _, p := range generated {
		if p.Kind == "unreachable_asset" {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Fatal("the planted orphan memory file produced no finding - this test would otherwise pass vacuously, having scanned nothing real")
	}

	after := hashTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("file count under the fixture tree changed: had %d, now %d - something was created or deleted", len(before), len(after))
	}
	for p, wantHash := range before {
		gotHash, ok := after[p]
		if !ok {
			t.Errorf("%s was deleted", p)
			continue
		}
		if gotHash != wantHash {
			t.Errorf("%s was modified - content hash changed", p)
		}
	}
}
