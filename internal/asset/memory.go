package asset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MemoryFile is one memory file discovered under any project's memory
// store, with enough detail for B7c's structural promotion-rule checks
// (docs/design.md, "B7c"). Unlike Discover, which scans only the current
// project, this is deliberately cross-project - duplicate detection and
// index-reachability only mean anything with that wider view. Constraint 6
// still holds: ContentHash and Links are derived, never the content itself.
type MemoryFile struct {
	// Store is the project slug this file's store belongs to - the
	// directory name under ~/.claude/projects, not a path.
	Store string
	Path  string
	// Filename is the basename without .md - what MEMORY.md's own links
	// resolve against.
	Filename string
	// Slug is the frontmatter name: field, empty if absent. What [[links]]
	// resolve against - a different identity than Filename, and the two
	// drifting apart is exactly what the filename-to-slug check looks for.
	Slug string
	// ContentHash is sha256 of the raw file bytes, hex-encoded - the
	// identity the cross-store duplicate check groups on. Never the
	// content itself.
	ContentHash string
	// Links is every [[slug]] reference the body makes, in the order
	// they appear. Resolved against Slug, not Filename.
	Links []string
}

// linkRe matches a [[slug]] reference the way memory files actually write
// them (confirmed on a real corpus: "See [[business-vision]] for full
// context."). Deliberately narrow - letters, digits, hyphens and
// underscores only, the same character set frontmatter slugs use - so
// ordinary double-bracketed text elsewhere is never mistaken for a link.
var linkRe = regexp.MustCompile(`\[\[([a-zA-Z0-9_-]+)\]\]`)

// memoryIndexName is the hand-written index file each store's own
// discovery already tolerates as an ordinary memory asset (a pre-existing
// quirk, not changed here - see scanMarkdownDir). DiscoverAllMemory excludes
// it from the file set the structural checks run against: it is the index,
// not a fact being indexed, and its own links point at every other file by
// design, which would make it a false hit on both the broken-link and the
// unreachable-asset checks.
const memoryIndexName = "MEMORY.md"

// DiscoverAllMemory walks every project's memory store under
// <home>/.claude/projects/*/memory and returns every memory file found
// except the index itself, hashed and parsed for B7c's structural checks.
// A missing or unreadable store is skipped, not an error - most stores
// will exist, not all (design doc constraint 1), and this scans dozens of
// stores at once, unlike Discover's single-project scan: one store with a
// permission problem must not take proposal listing down for every other
// store along with it (constraint 7, degrade never block - found by code
// review, an earlier version propagated any error past os.IsNotExist and
// let one bad store fail the whole scan).
//
// Two conventions are recognized side by side, mirroring scanMarkdownDir's
// own two shapes for this exact kind (KindMemory) in the single-project
// Discover path: a plain "name.md" file directly in the store, or a
// subdirectory containing its own SKILL.md. Also found by code review -
// without this, a memory asset using the subdirectory convention was
// invisible to every B7c check, including a false broken_link report
// against a [[link]] whose target genuinely existed.
func DiscoverAllMemory(home string) ([]MemoryFile, error) {
	root := filepath.Join(home, ".claude", "projects")
	stores, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []MemoryFile
	for _, s := range stores {
		if !s.IsDir() {
			continue
		}
		memDir := filepath.Join(root, s.Name(), "memory")
		entries, err := os.ReadDir(memDir)
		if err != nil {
			continue // this store is unreadable; every other store still gets scanned
		}
		for _, e := range entries {
			if e.IsDir() {
				path := filepath.Join(memDir, e.Name(), "SKILL.md")
				if _, err := os.Stat(path); err != nil {
					continue // a subdirectory with no SKILL.md isn't a memory asset
				}
				if mf, ok := readMemoryFile(path, s.Name(), e.Name()); ok {
					out = append(out, mf)
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".md") || e.Name() == memoryIndexName {
				continue
			}
			path := filepath.Join(memDir, e.Name())
			if mf, ok := readMemoryFile(path, s.Name(), strings.TrimSuffix(e.Name(), ".md")); ok {
				out = append(out, mf)
			}
		}
	}
	return out, nil
}

// readMemoryFile reads path and builds a MemoryFile, or reports false if
// the file is unreadable - not grounds for failing the whole scan. filename
// is passed in rather than derived from path, because the two file-shape
// conventions disagree on what it should be: a flat "name.md" derives it
// from its own basename, but a directory-convention "name/SKILL.md" derives
// it from the directory, not the literal "SKILL" basename.
func readMemoryFile(path, store, filename string) (MemoryFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MemoryFile{}, false
	}
	fm := readFrontmatter(path)
	sum := sha256.Sum256(data)

	var links []string
	for _, m := range linkRe.FindAllStringSubmatch(string(data), -1) {
		links = append(links, m[1])
	}

	return MemoryFile{
		Store: store, Path: path, Filename: filename, Slug: fm.Name,
		ContentHash: hex.EncodeToString(sum[:]), Links: links,
	}, true
}

// MemoryIndex reads one store's MEMORY.md and returns the set of filenames
// it links to (the target of a markdown link, extension stripped) - what
// the unreachable-asset check treats as "reachable". A missing,
// unreadable, or truncated-mid-read MEMORY.md all mean the same thing here:
// an empty, not-nil index. Every file in that store is unreachable by it
// either way, which is itself the finding, not a reason to fail this
// store's scan, let alone every other store's (constraint 7). No error to
// return as a result: every failure mode this function can hit collapses
// to the same empty-index answer.
func MemoryIndex(home, store string) map[string]bool {
	path := filepath.Join(home, ".claude", "projects", store, "memory", memoryIndexName)
	f, err := os.Open(path)
	if err != nil {
		return map[string]bool{}
	}
	defer func() { _ = f.Close() }()

	index := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, m := range mdLinkRe.FindAllStringSubmatch(sc.Text(), -1) {
			index[strings.TrimSuffix(m[1], ".md")] = true
		}
	}
	if sc.Err() != nil {
		return map[string]bool{}
	}
	return index
}

// mdLinkRe matches a markdown link's target, the shape MEMORY.md's own
// index entries use (confirmed on a real corpus: "- [Title](filename.md) -
// hook"). Deliberately targets the parenthesized href only, never the link
// text, which is free-form prose Loom has no reason to parse.
var mdLinkRe = regexp.MustCompile(`\]\(([a-zA-Z0-9_.-]+\.md)\)`)
