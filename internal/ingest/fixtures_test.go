package ingest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This is the fixture-hygiene check the publishability rules require before
// the repository can go public (docs/design.md). It lives as a test rather
// than a separate CI job so it runs wherever `go test` does, with no new
// machinery to maintain.
//
// It is named hygiene, not provenance, deliberately. Nothing can prove from
// content that a file was hand-written rather than derived from a real
// transcript. What this catches is the *markers* of real data, which is a
// weaker claim and is the one being made. The actual claim that each fixture
// was written by hand is the manifest in testdata/README.md, which is
// reviewable in a diff.

const fixtureRoot = "../../testdata"

// maxFixtureBytes is generous against anything hand-written and tight against
// anything real. Every current fixture is under 2.5KB; the session transcript
// this check was written during was 78MB.
const maxFixtureBytes = 32 * 1024

// maxUnbrokenRun catches copied real content. A real `thinking` block carries a
// signature field running to tens of kilobytes with no whitespace; nothing
// anyone types by hand looks like that.
const maxUnbrokenRun = 500

var (
	homePathRe = regexp.MustCompile(`/(?:Users|home)/[a-zA-Z][a-zA-Z0-9._-]*`)
	emailRe    = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	// Fixture filenames referenced from the manifest table, as `backticked`
	// paths relative to testdata/. Must start with an alphanumeric so prose
	// mentioning an extension (the `.meta.json` companion) is not mistaken for
	// a manifest entry - which it was, on the first run.
	manifestEntryRe = regexp.MustCompile("`([a-zA-Z0-9][a-zA-Z0-9._/-]*\\.(?:jsonl|json))`")
)

// fixtureFiles lists every fixture on disk, relative to testdata/.
func fixtureFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(fixtureRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".json") {
			return nil
		}
		rel, err := filepath.Rel(fixtureRoot, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFixtureHygiene(t *testing.T) {
	files := fixtureFiles(t)
	if len(files) == 0 {
		t.Fatal("no fixtures found - the check is pointing at the wrong directory")
	}

	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(fixtureRoot, rel))
			if err != nil {
				t.Fatal(err)
			}
			body := string(b)

			if len(b) > maxFixtureBytes {
				t.Errorf("%d bytes, over the %d ceiling - hand-written fixtures are small, real "+
					"transcripts are not", len(b), maxFixtureBytes)
			}
			if m := homePathRe.FindString(body); m != "" {
				t.Errorf("contains an absolute home path %q - a marker of real data", m)
			}
			if m := emailRe.FindString(body); m != "" {
				t.Errorf("contains what looks like an email address %q", m)
			}
			for _, field := range strings.FieldsFunc(body, func(r rune) bool {
				return r == ' ' || r == '\n' || r == '\t' || r == '"' || r == ','
			}) {
				if len(field) > maxUnbrokenRun {
					t.Errorf("contains a %d-character unbroken string - real thinking signatures "+
						"look like this, hand-written fixtures do not", len(field))
					break
				}
			}
		})
	}
}

// TestFixtureManifestIsComplete is the part that carries the real claim.
// Adding a fixture without declaring it in testdata/README.md fails here, so
// the assertion that it was hand-written is made explicitly and shows up in a
// diff, rather than being assumed by its absence.
func TestFixtureManifestIsComplete(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join(fixtureRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]bool{}
	for _, m := range manifestEntryRe.FindAllStringSubmatch(string(readme), -1) {
		declared[m[1]] = true
	}

	for _, rel := range fixtureFiles(t) {
		if !declared[rel] {
			t.Errorf("fixture %q is not in the manifest in testdata/README.md. Add it, and in "+
				"doing so state that it was written by hand", rel)
		}
	}
	for name := range declared {
		if _, err := os.Stat(filepath.Join(fixtureRoot, name)); err != nil {
			t.Errorf("manifest lists %q, which does not exist - a stale manifest is worse than "+
				"none, because it looks like a claim", name)
		}
	}
}
