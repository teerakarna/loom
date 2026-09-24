package propose

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/teerakarna/loom/internal/asset"
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

	// KindUnreachableAsset suggests a memory file that exists on disk
	// but is not linked from its store's own MEMORY.md index - reachable
	// on disk, unreachable through the mechanism meant to surface it.
	KindUnreachableAsset = "unreachable_asset"

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

// LaneScopedKinds are the four B7c memory-finding kinds - unambiguously
// per-store, so per-lane (issue #68) and per-scan-coverage (issue #59).
// Every other kind's evidence is not store-shaped at all. A future
// per-store finding kind must be added here too, or it silently loses both
// --lane filtering and needsProtection's transient-failure protection -
// nothing else enforces that (flagged by code review, not yet a bug since
// no fifth kind exists).
var LaneScopedKinds = map[string]bool{
	KindPromoteMemoryDuplicate: true,
	KindBrokenLink:             true,
	KindUnreachableAsset:       true,
	KindFilenameSlugDrift:      true,
}

// EvidenceStores extracts the store(s) a lane-scoped proposal's evidence
// claims to belong to - "store" for three of the four kinds, "stores" (a
// list) for KindPromoteMemoryDuplicate, whose finding inherently spans
// every store the duplicate appears in (found by code review, issue #68 -
// checking "store" alone silently dropped every duplicate-kind proposal
// from every --lane view, regardless of lane). Returns nil for anything
// that fails to parse or names no store - callers decide what "unknown"
// means for their own purpose.
func EvidenceStores(evidence string) []string {
	var ev struct {
		Store  string   `json:"store"`
		Stores []string `json:"stores"`
	}
	if err := json.Unmarshal([]byte(evidence), &ev); err != nil {
		return nil
	}
	if ev.Store != "" {
		return []string{ev.Store}
	}
	return ev.Stores
}

// GenerateMemoryFindings scans every project's memory store for B7c's four
// structural promotion-rule checks and returns whatever proposals the
// evidence supports, in a stable order - see detectMemoryDuplicates for why
// that has to be stated explicitly rather than assumed. Separate from
// Generate deliberately: this is the one place in propose that needs
// filesystem access (DiscoverAllMemory is cross-project, unlike anything
// Generate's own DB-only checks touch), and keeping it out of Generate
// means every existing caller and test of Generate(db, now) is unaffected
// by this slice.
//
// Constraint 10 (every accelerator ships with its brake): generation cap and
// dedupe key are inherited, not separately implemented. Every proposal
// returned here goes through the same Store/UpsertProposal path as B5's
// three kinds - the same ledger.MaxPendingProposals cap (20) and the same
// (kind, subject)-keyed, evidence-hash re-raise rule apply uniformly. No
// content is retained: evidence carries paths, slugs and a content hash,
// never file bodies.
//
// The second return value is DiscoverAllMemory's own scan coverage report,
// passed straight through for Store to use protecting a skipped store's
// pending proposals from a false withdrawal (issue #59) - this function has
// nothing else to add to it, so it does not touch it, only forwards it.
func GenerateMemoryFindings(home string) ([]Proposal, asset.MemoryScanCoverage, error) {
	files, coverage, err := asset.DiscoverAllMemory(home)
	if err != nil {
		return nil, asset.MemoryScanCoverage{}, err
	}

	// A skill-directory read problem degrades to "no known skills" rather
	// than failing this whole pass (constraint 7) - it only narrows
	// detectBrokenLinks back to issue #67's behavior (silent on a link
	// that resolves nowhere), never turns a real finding into an error.
	knownSkills := map[string]bool{}
	if skills, err := asset.DiscoverGlobalSkills(home); err == nil {
		for _, s := range skills {
			knownSkills[s.Name] = true
		}
	}

	// Root-cause kinds before the symptom kind: a filename/slug drift is
	// often *why* a link elsewhere is broken, and the cheaper fix. Order
	// matters once the pending cap (interleaveByKind, propose.go) has to
	// break a tie at the margin - see issue #65.
	var out []Proposal
	out = append(out, detectMemoryDuplicates(files)...)
	out = append(out, detectFilenameSlugDrift(files)...)
	out = append(out, detectUnreachableAssets(home, files)...)
	out = append(out, detectBrokenLinks(home, files, knownSkills)...)
	return out, coverage, nil
}

// detectMemoryDuplicates groups files by content hash and proposes
// promoting any group spanning three or more distinct stores. Deliberately
// simple: hashing only, no similarity threshold, no judgement about
// content - a real duplicate resolves 1:1 on this signal.
//
// Sorted by hash before returning - found by code review: grouping by a Go
// map and ranging over it directly means the order proposals are generated
// in is randomized per call, which matters once the total across every
// B7c check exceeds the pending-proposal cap: UpsertProposal accepts new
// subjects in the order Store sees them, so an unsorted order meant two
// back-to-back runs against identical, unchanged disk state could persist
// a different subset of duplicate findings each time.
func detectMemoryDuplicates(files []asset.MemoryFile) []Proposal {
	byHash := map[string][]asset.MemoryFile{}
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
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// detectBrokenLinks checks every [[slug]] reference against the slugs that
// actually exist in the same store - links resolve within one store, the
// same scope memory itself loads in. Iterates files in the order
// DiscoverAllMemory produced them, which is disk order (os.ReadDir sorts by
// name), so no separate sort is needed here the way detectMemoryDuplicates
// needs one.
//
// knownSkills is every skill/reference name DiscoverGlobalSkills found
// (issue #66): a link that resolves nowhere as a memory is normally a
// permitted forward reference (issue #67) - but if the name is a known
// skill instead, it will never become a memory, since a [[link]] only ever
// resolves against a memory's own frontmatter name. Real on a real corpus:
// [[entity-team]] named a skill directory, not a not-yet-written note, and
// staying silent about it (issue #67's own default for "resolves nowhere")
// told the reader nothing useful about a reference that was never going to
// resolve as written.
func detectBrokenLinks(home string, files []asset.MemoryFile, knownSkills map[string]bool) []Proposal {
	knownSlugs := map[string]map[string]bool{}
	// existsAnywhere is the same slugs, flattened across every store - what
	// distinguishes a real broken link from a forward reference (issue #67).
	// Claude Code's own memory convention says a [[link]] to a slug that
	// doesn't exist yet is fine, a deliberate marker for something worth
	// writing later, not rot: "a [[name]] that doesn't match an existing
	// memory yet is fine". Measured on a real corpus: 185 of 214 broken_link
	// findings (86%) were this class. A target that exists nowhere at all is
	// indistinguishable on disk from a not-yet-written forward reference, so
	// it is not flagged; a target that exists in a different store is a real
	// finding (the reference is real, just scoped wrong), and still is.
	existsAnywhere := map[string]bool{}
	for _, f := range files {
		if f.Slug == "" {
			continue
		}
		if knownSlugs[f.Store] == nil {
			knownSlugs[f.Store] = map[string]bool{}
		}
		knownSlugs[f.Store][f.Slug] = true
		existsAnywhere[f.Slug] = true
	}

	// Cached per store, not checked once globally: HasMemoryIndex costs one
	// os.Stat, cheap, but every file in a store would otherwise repeat it.
	hasIndex := map[string]bool{}

	var out []Proposal
	for _, f := range files {
		for _, link := range f.Links {
			// The store's own index is deliberately excluded from the file
			// set DiscoverAllMemory returns (it is the index, not a fact
			// being indexed - see memoryIndexName), so it never gets a Slug
			// to register here. Without this, a real, legitimate reference
			// to it - "[[MEMORY]] for the full index" - reads as broken
			// forever, with no correct resolution possible (issue #65). But
			// only when the index actually exists - a store with no
			// MEMORY.md at all has nothing for [[MEMORY]] to resolve to,
			// and that is still a real broken link, not a free pass just
			// because it names the conventional target (found by code
			// review, before this shipped).
			if link == asset.MemoryIndexSlug {
				if _, cached := hasIndex[f.Store]; !cached {
					hasIndex[f.Store] = asset.HasMemoryIndex(home, f.Store)
				}
				if hasIndex[f.Store] {
					continue
				}
			} else if knownSlugs[f.Store][link] {
				// Resolves here - not broken.
				continue
			} else if !existsAnywhere[link] && !knownSkills[link] {
				// Resolves nowhere at all, and isn't a known skill either -
				// a permitted forward reference (issue #67), and looks
				// identical on disk to real rot; the evidence to tell them
				// apart is intent, which the filesystem does not carry.
				continue
			}
			subject := f.Store + "/" + f.Filename + " -> " + link
			summary := fmt.Sprintf("%s links to [[%s]], which exists but not in this store", f.Filename, link)
			rationale := "A [[link]] resolves against another memory file's frontmatter name in the " +
				"same store. A file with this name exists, just not here - the reference is real, " +
				"scoped to the wrong store. Loom will not edit it: this is a suggestion to fix the " +
				"reference yourself."
			targetIsSkill := !existsAnywhere[link] && knownSkills[link]
			switch {
			// The [[MEMORY]] case that falls through here (no index in this
			// store at all) is not the cross-store case above: the target
			// doesn't exist anywhere, not "just not here" - found by code
			// review, before this shipped, sharing the wrong wording with
			// the general case.
			case link == asset.MemoryIndexSlug:
				summary = fmt.Sprintf("%s links to [[MEMORY]], but this store has no MEMORY.md", f.Filename)
				rationale = "A [[MEMORY]] link resolves to the store's own index file, which does not " +
					"exist here. Loom will not create it: this is a suggestion to add one, or fix the " +
					"reference if the store was never meant to have one."
			// Checked after existsAnywhere, not before: a real memory slug
			// in another store is the more specific, more actionable
			// finding (issue #67's original case) even on the rare chance
			// a skill happens to share its name (issue #66).
			case targetIsSkill:
				summary = fmt.Sprintf("%s links to [[%s]], which is a skill, not a memory", f.Filename, link)
				rationale = "A [[link]] resolves only against another memory file's frontmatter name in " +
					"the same store - never a skill, agent, or other asset kind, even one with a " +
					"matching name. A skill named this exists, but this reference will never resolve as " +
					"written. Loom will not edit it: point at the skill by name in ordinary prose " +
					"instead of [[link]] syntax, or write the memory this was meant to reference."
			}
			// target_is_skill is stored, not just carried in the in-process
			// Summary/Rationale strings above, because both are rebuilt from
			// evidence alone at display time (SummaryFor), never read back
			// off this Proposal - the skill-shadow and cross-store cases
			// would otherwise be indistinguishable on a stored row, and
			// SummaryFor would always guess the wrong one (found by code
			// review, before this shipped).
			out = append(out, Proposal{
				Kind: KindBrokenLink, Subject: subject,
				Evidence: map[string]any{
					"store": f.Store, "filename": f.Filename, "target_slug": link,
					"target_is_skill": targetIsSkill,
				},
				Summary:   summary,
				Rationale: rationale,
			})
		}
	}
	return out
}

// detectFilenameSlugDrift flags a memory file whose filename no longer
// matches its own frontmatter name - the exact mismatch that makes
// [[links]] to it break, since links resolve against the name, and any
// human guessing at the link target from the filename will guess wrong.
// Disk order, same reasoning as detectBrokenLinks.
func detectFilenameSlugDrift(files []asset.MemoryFile) []Proposal {
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
			Summary: fmt.Sprintf("%s's filename no longer matches its own name: %s", f.Filename, f.Slug),
			Rationale: "This file's frontmatter name field - what [[links]] to it actually resolve " +
				"against - has drifted from its filename. Loom will not rename it: this is a " +
				"suggestion to bring the two back in line yourself.",
		})
	}
	return out
}

// detectUnreachableAssets checks every file against its own store's
// MEMORY.md index. A store with no index at all means every file in it is
// unreachable by that mechanism, which is the finding, not a reason to
// skip the store. Disk order, same reasoning as detectBrokenLinks; the
// per-store index cache preserves that order (a Go map keyed by store, but
// files are visited in the order DiscoverAllMemory produced them, not by
// ranging over the cache).
func detectUnreachableAssets(home string, files []asset.MemoryFile) []Proposal {
	indexes := map[string]map[string]bool{}
	var out []Proposal
	for _, f := range files {
		idx, ok := indexes[f.Store]
		if !ok {
			idx = asset.MemoryIndex(home, f.Store)
			indexes[f.Store] = idx
		}
		if idx[f.Filename] {
			continue
		}
		out = append(out, Proposal{
			Kind: KindUnreachableAsset, Subject: f.Store + "/" + f.Filename,
			Evidence: map[string]any{
				"store": f.Store, "filename": f.Filename,
			},
			Summary: fmt.Sprintf("%s exists but is not linked from its store's MEMORY.md", f.Filename),
			Rationale: "On disk, but not reachable through the index that is meant to surface it. " +
				"Loom will not add the link: this is a suggestion to add it yourself, or confirm the " +
				"file is no longer needed.",
		})
	}
	return out
}

// distinctStores returns the set of stores group spans, as a slice (not a
// map) since callers need a stable, sortable list for evidence.
func distinctStores(group []asset.MemoryFile) []string {
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
