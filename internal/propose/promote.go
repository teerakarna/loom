package propose

import (
	"fmt"
	"sort"

	"github.com/teerakarna/loom/internal/artifact"
)

// Proposal kinds for B7c (#41): promotion rules as read-only proposals.
// All four touch the user's own memory files, so all four are rendered and
// never applied - see TouchesUserFiles.
const (
	// KindPromoteMemoryDuplicate suggests a memory file that exists
	// byte-identically across three or more project stores is a
	// cross-project fact stuck in a per-project mechanism, and belongs as
	// a reference skill instead. Hashing only: no threshold beyond "three
	// or more stores", no recurrence inference, no content retained.
	KindPromoteMemoryDuplicate = "promote_memory_duplicate"

	// KindBrokenLink suggests a [[slug]] reference in a memory file that
	// does not resolve to any memory file's own frontmatter name in the
	// same store.
	KindBrokenLink = "broken_link"

	// KindUnreachableArtifact suggests a memory file that exists on disk
	// but is not linked from its store's own MEMORY.md index - reachable
	// on disk, unreachable through the mechanism meant to surface it.
	KindUnreachableArtifact = "unreachable_artifact"

	// KindFilenameSlugDrift suggests a memory file whose filename no
	// longer matches its own frontmatter name - the mismatch [[links]]
	// elsewhere actually break against.
	KindFilenameSlugDrift = "filename_slug_drift"
)

// minDuplicateStores is the threshold docs/design.md states for B7c's
// memory-duplicate rule: three or more stores, not two. Two stores sharing
// a fact is plausibly a coincidence or a deliberate copy; three is a
// pattern worth surfacing.
const minDuplicateStores = 3

// GenerateMemoryFindings scans every project's memory store for B7c's four
// structural promotion-rule checks and returns whatever proposals the
// evidence supports. Separate from Generate deliberately: this is the one
// place in propose that needs filesystem access (DiscoverAllMemory is
// cross-project, unlike anything Generate's own DB-only checks touch), and
// keeping it out of Generate means every existing caller and test of
// Generate(db, now) is unaffected by this slice.
//
// Constraint 10 (every accelerator ships with its brake): generation cap and
// dedupe key are inherited, not separately implemented. Every proposal
// returned here goes through the same Store/UpsertProposal path as B5's
// three kinds - the same ledger.MaxPendingProposals cap (20) and the same
// (kind, subject)-keyed, evidence-hash re-raise rule apply uniformly. No
// content is retained: evidence carries paths, slugs and a content hash,
// never file bodies.
func GenerateMemoryFindings(home string) ([]Proposal, error) {
	files, err := artifact.DiscoverAllMemory(home)
	if err != nil {
		return nil, err
	}

	var out []Proposal
	out = append(out, detectMemoryDuplicates(files)...)
	out = append(out, detectBrokenLinks(files)...)
	out = append(out, detectFilenameSlugDrift(files)...)

	unreachable, err := detectUnreachableArtifacts(home, files)
	if err != nil {
		return nil, err
	}
	out = append(out, unreachable...)

	return out, nil
}

// detectMemoryDuplicates groups files by content hash and proposes
// promoting any group spanning three or more distinct stores. Deliberately
// simple: hashing only, no similarity threshold, no judgement about
// content - a real duplicate resolves 1:1 on this signal.
func detectMemoryDuplicates(files []artifact.MemoryFile) []Proposal {
	byHash := map[string][]artifact.MemoryFile{}
	for _, f := range files {
		byHash[f.ContentHash] = append(byHash[f.ContentHash], f)
	}

	var out []Proposal
	for hash, group := range byHash {
		stores := distinctStores(group)
		if len(stores) < minDuplicateStores {
			continue
		}
		sort.Strings(stores)
		var paths []string
		for _, f := range group {
			paths = append(paths, f.Store+"/"+f.Filename)
		}
		sort.Strings(paths)

		out = append(out, Proposal{
			Kind: KindPromoteMemoryDuplicate, Subject: hash,
			Evidence: map[string]any{
				"filename": group[0].Filename, "stores": stores, "paths": paths,
			},
			SampleSize: len(stores),
			Summary: fmt.Sprintf("promote %q to a reference skill, identical across %d stores",
				group[0].Filename, len(stores)),
			Rationale: "A memory file that exists byte-identically across three or more project " +
				"stores is a cross-project fact stuck in a per-project mechanism. Memory loads only " +
				"for its own project, so this fact is invisible everywhere it isn't. Loom will not " +
				"move it: this is a suggestion to promote it to a reference skill yourself.",
		})
	}
	return out
}

// detectBrokenLinks checks every [[slug]] reference against the slugs that
// actually exist in the same store - links resolve within one store, the
// same scope memory itself loads in.
func detectBrokenLinks(files []artifact.MemoryFile) []Proposal {
	knownSlugs := map[string]map[string]bool{}
	for _, f := range files {
		if f.Slug == "" {
			continue
		}
		if knownSlugs[f.Store] == nil {
			knownSlugs[f.Store] = map[string]bool{}
		}
		knownSlugs[f.Store][f.Slug] = true
	}

	var out []Proposal
	for _, f := range files {
		for _, link := range f.Links {
			if knownSlugs[f.Store][link] {
				continue
			}
			subject := f.Store + "/" + f.Filename + " -> " + link
			out = append(out, Proposal{
				Kind: KindBrokenLink, Subject: subject,
				Evidence: map[string]any{
					"store": f.Store, "filename": f.Filename, "target_slug": link,
				},
				SampleSize: 0,
				Summary:    fmt.Sprintf("%s links to [[%s]], which does not exist in its store", f.Filename, link),
				Rationale: "A [[link]] resolves against another memory file's frontmatter name in the " +
					"same store. No file with this name was found there - the target was renamed, " +
					"moved, or never existed under that name. Loom will not edit it: this is a " +
					"suggestion to fix the reference yourself.",
			})
		}
	}
	return out
}

// detectFilenameSlugDrift flags a memory file whose filename no longer
// matches its own frontmatter name - the exact mismatch that makes
// [[links]] to it break, since links resolve against the name, and any
// human guessing at the link target from the filename will guess wrong.
func detectFilenameSlugDrift(files []artifact.MemoryFile) []Proposal {
	var out []Proposal
	for _, f := range files {
		if f.Slug == "" || f.Slug == f.Filename {
			continue
		}
		out = append(out, Proposal{
			Kind: KindFilenameSlugDrift, Subject: f.Store + "/" + f.Filename,
			Evidence: map[string]any{
				"store": f.Store, "filename": f.Filename, "slug": f.Slug,
			},
			SampleSize: 0,
			Summary:    fmt.Sprintf("%s's filename no longer matches its own name: %s", f.Filename, f.Slug),
			Rationale: "This file's frontmatter name field - what [[links]] to it actually resolve " +
				"against - has drifted from its filename. Loom will not rename it: this is a " +
				"suggestion to bring the two back in line yourself.",
		})
	}
	return out
}

// detectUnreachableArtifacts checks every file against its own store's
// MEMORY.md index. A store with no index at all means every file in it is
// unreachable by that mechanism, which is the finding, not a reason to
// skip the store.
func detectUnreachableArtifacts(home string, files []artifact.MemoryFile) ([]Proposal, error) {
	indexes := map[string]map[string]bool{}
	var out []Proposal
	for _, f := range files {
		idx, ok := indexes[f.Store]
		if !ok {
			var err error
			idx, err = artifact.MemoryIndex(home, f.Store)
			if err != nil {
				return nil, err
			}
			indexes[f.Store] = idx
		}
		if idx[f.Filename] {
			continue
		}
		out = append(out, Proposal{
			Kind: KindUnreachableArtifact, Subject: f.Store + "/" + f.Filename,
			Evidence: map[string]any{
				"store": f.Store, "filename": f.Filename,
			},
			SampleSize: 0,
			Summary:    fmt.Sprintf("%s exists but is not linked from its store's MEMORY.md", f.Filename),
			Rationale: "On disk, but not reachable through the index that is meant to surface it. " +
				"Loom will not add the link: this is a suggestion to add it yourself, or confirm the " +
				"file is no longer needed.",
		})
	}
	return out, nil
}

// distinctStores returns the set of stores group spans, as a slice (not a
// map) since callers need a stable, sortable list for evidence.
func distinctStores(group []artifact.MemoryFile) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range group {
		if !seen[f.Store] {
			seen[f.Store] = true
			out = append(out, f.Store)
		}
	}
	return out
}
