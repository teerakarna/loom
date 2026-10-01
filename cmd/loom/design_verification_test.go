package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestEveryVerificationItemNamesARealTest is what makes docs/design.md's
// "## Verification" section machine-checkable rather than aspirational
// prose: every backtick-quoted `TestXxx` name in that section (and its "###
// Added for B7" subsection) must exist as a real top-level test function
// somewhere in the module, or CI fails. This is the exact drift the section
// itself names as the reason for existing: the planted-secret test
// (`TestNoContentStored`) was required from B1 and nothing ran it for five
// milestones, found by accident in B7b rather than by anything that would
// have caught a rename or deletion the moment it happened.
//
// Deliberately coarse: it confirms the named function exists somewhere in
// the repo, not that it lives in the specific file the doc also names next
// to it - several items cite one shared file for multiple test names, and a
// stricter (name, file) pairing would need a much more rigid prose format
// than this section is written in for a marginal gain. A renamed or deleted
// test still fails this exactly as hard; a test that quietly moved to a
// different file within the same package does not, and that gap is
// accepted rather than forcing the doc into a less readable shape to close
// it.
func TestEveryVerificationItemNamesARealTest(t *testing.T) {
	designPath := filepath.Join("..", "..", "docs", "design.md")
	doc, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}

	section := extractVerificationSection(t, string(doc))

	named := extractBacktickedTestNames(section)
	if len(named) < 10 {
		t.Fatalf("found only %d named tests in the Verification section, want at least 10 - "+
			"the section heading or backtick format likely changed and this extraction silently broke", len(named))
	}

	realTests, err := collectRealTestNames(t, "..", "..")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range named {
		if !realTests[name] {
			t.Errorf("docs/design.md's Verification section names %s, but no such test exists anywhere in the module - "+
				"it was renamed or deleted without the doc being updated to match", name)
		}
	}
}

// extractVerificationSection returns the text between the "## Verification"
// heading and the next "## " heading (or end of file), including its "###
// Added for B7" subsection.
func extractVerificationSection(t *testing.T, doc string) string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^## Verification$`).FindStringIndex(doc)
	if start == nil {
		t.Fatal("docs/design.md has no \"## Verification\" heading - did it get renamed?")
	}
	rest := doc[start[1]:]
	if end := regexp.MustCompile(`(?m)^## `).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

// testNamePattern matches a backtick-quoted Go test function name
// specifically - anchored so a backtick-quoted file path or package name
// elsewhere in the section's prose is never mistaken for one.
var testNamePattern = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

func extractBacktickedTestNames(section string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range testNamePattern.FindAllStringSubmatch(section, -1) {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// funcTestPattern matches a top-level test function definition.
var funcTestPattern = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

// collectRealTestNames walks every *_test.go file under the given module-
// relative directory segments and returns the set of top-level TestXxx
// function names actually defined anywhere in the module.
func collectRealTestNames(t *testing.T, elem ...string) (map[string]bool, error) {
	t.Helper()
	root := filepath.Join(elem...)
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isTestGoFile(path) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range funcTestPattern.FindAllStringSubmatch(string(content), -1) {
			out[m[1]] = true
		}
		return nil
	})
	return out, err
}

func isTestGoFile(path string) bool {
	const suffix = "_test.go"
	return len(path) > len(suffix) && path[len(path)-len(suffix):] == suffix
}
