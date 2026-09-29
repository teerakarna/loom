package propose

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/azva-co/loom/internal/asset"
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

// linkTargetNames returns every name a [[link]] author might plausibly
// reference for a: both the frontmatter name assetFromFile resolved and
// the name actually on disk (asset.Asset.OnDiskName), which can drift
// apart (a real, documented failure mode - found by code review on #66,
// before this shipped, citing this project's own memory of six skills
// sitting with drifted frontmatter for months unnoticed). The on-disk name
// is what a human actually invokes, not necessarily whatever the
// frontmatter claims.
//
// Reads OnDiskName rather than re-deriving it from Path (a first version
// of this function sniffed the basename for a literal "SKILL.md" to tell
// scanMarkdownDir's two shapes apart) - found by code review, before this
// shipped: that duplicated a computation assetFromFile's own caller had
// already done once and discarded, and would silently go stale if that
// convention ever changed without linkTargetNames changing to match.
func linkTargetNames(a asset.Asset) []string {
	if a.OnDiskName == a.Name {
		return []string{a.Name}
	}
	return []string{a.Name, a.OnDiskName}
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

	// A read problem on any one of these directories degrades to "nothing
	// known for that kind" rather than failing this whole pass (constraint
	// 7) - it only narrows detectBrokenLinks back to issue #67's behavior
	// (silent on a link that resolves nowhere) for that kind, never turns a
	// real finding into an error.
	//
	// knownOtherAssets maps a name to the one kind (skill, agent or plan)
	// it belongs to - every kind a [[link]] can name but never actually
	// resolve against, not just skills (issue #78, generalizing #66's
	// skill-only case; agents and plans have the identical global-plus-
	// per-project shape in asset.DefaultLocations, and only the global half
	// is reliably enumerable the same way skills' is). A name colliding
	// across two kinds is vanishingly unlikely, but "whichever happens to
	// register last wins" is an accident of call order, not a real answer -
	// first registration wins instead, so the precedence is the fixed,
	// documented order the calls below are written in (skill, then agent,
	// then plan), not implementation-order-dependent (found by code review,
	// before this shipped).
	knownOtherAssets := map[string]string{}
	registerOtherAssets := func(kind string, discover func(string) ([]asset.Asset, error)) {
		found, err := discover(home)
		if err != nil {
			return
		}
		for _, a := range found {
			if a.Kind != kind {
				// KindSkill only, not KindReference: a flat .md file
				// directly in the skills directory is never loadable as a
				// skill (scanSkillDir's own doc comment) - calling it one
				// would be factually wrong in the rationale text (found by
				// code review on #66, before this shipped).
				continue
			}
			for _, name := range linkTargetNames(a) {
				if _, taken := knownOtherAssets[name]; taken {
					continue
				}
				knownOtherAssets[name] = kind
			}
		}
	}
	registerOtherAssets(asset.KindSkill, asset.DiscoverGlobalSkills)
	registerOtherAssets(asset.KindAgent, asset.DiscoverGlobalAgents)
	registerOtherAssets(asset.KindPlan, asset.DiscoverGlobalPlans)

	// Root-cause kinds before the symptom kind: a filename/slug drift is
	// often *why* a link elsewhere is broken, and the cheaper fix. Order
	// matters once the pending cap (interleaveByKind, propose.go) has to
	// break a tie at the margin - see issue #65.
	var out []Proposal
	out = append(out, detectMemoryDuplicates(files)...)
	out = append(out, detectFilenameSlugDrift(files)...)
	out = append(out, detectUnreachableAssets(home, files)...)
	out = append(out, detectBrokenLinks(home, files, knownOtherAssets)...)
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

// brokenLinkKindNoun says how each "other asset" kind reads in a sentence
// (issue #78, generalizing #66's skill-only case) - the article included,
// since it is not guessable from the kind string alone.
// A future "other asset" kind added to registerOtherAssets' call list needs
// an entry here too, or brokenLinkText's lookup below silently falls back
// to the raw kind string rather than failing loudly - nothing else
// enforces that they stay matched (flagged by code review, not yet a bug
// since every kind registerOtherAssets can produce has one).
var brokenLinkKindNoun = map[string]string{
	asset.KindSkill: "a skill",
	asset.KindAgent: "an agent",
	asset.KindPlan:  "a plan",
}

// brokenLinkText builds a broken_link finding's display text from its
// classification alone - the [[MEMORY]] special case (issue #65), the
// other-asset-kind case (issues #66, #78), or the plain cross-store case
// (issue #67) - never from a fresh scan. Shared by detectBrokenLinks (which
// has fresh scan data) and SummaryFor (which only has stored evidence) so
// the two can never drift into different wording for the same
// classification (found by code review on #66, before this shipped: they
// were duplicated in structurally different shapes before this).
func brokenLinkText(filename, targetSlug any, isMemoryIndex bool, otherKind string) (summary, rationale string) {
	switch {
	case isMemoryIndex:
		return fmt.Sprintf("%v links to [[MEMORY]], but this store has no MEMORY.md", filename),
			"A [[MEMORY]] link resolves to the store's own index file, which does not exist here. " +
				"Loom will not create it: this is a suggestion to add one, or fix the reference if the " +
				"store was never meant to have one."
	case otherKind != "":
		noun := brokenLinkKindNoun[otherKind]
		if noun == "" {
			// A kind with no entry above - degrade to the raw kind string
			// rather than an empty one ("which is , not a memory"), so a
			// forgotten entry still reads as a defect worth reporting, not
			// as a malformed sentence nobody would think to file a bug for.
			noun = otherKind
		}
		return fmt.Sprintf("%v links to [[%v]], which is %s, not a memory", filename, targetSlug, noun),
			fmt.Sprintf("A [[link]] resolves only against another memory file's frontmatter name in the "+
				"same store - never a skill, agent, plan, or other asset kind, even one with a matching "+
				"name. This link's target is %s, but the reference will never resolve as written. Loom "+
				"will not edit it: point at it by name in ordinary prose instead of [[link]] syntax, or "+
				"write the memory this was meant to reference.", noun)
	default:
		return fmt.Sprintf("%v links to [[%v]], which exists but not in this store", filename, targetSlug),
			"A [[link]] resolves against another memory file's frontmatter name in the same store. A " +
				"file with this name exists, just not here - the reference is real, scoped to the wrong " +
				"store. Loom will not edit it: this is a suggestion to fix the reference yourself."
	}
}

// detectBrokenLinks checks every [[slug]] reference against the slugs that
// actually exist in the same store - links resolve within one store, the
// same scope memory itself loads in. Iterates files in the order
// DiscoverAllMemory produced them, which is disk order (os.ReadDir sorts by
// name), so no separate sort is needed here the way detectMemoryDuplicates
// needs one.
//
// knownOtherAssets maps a name to the skill, agent or plan it belongs to
// (issues #66, #78): a link that resolves nowhere as a memory is normally a
// permitted forward reference (issue #67) - but if the name already
// belongs to one of those, it will never become a memory either, since a
// [[link]] only ever resolves against a memory's own frontmatter name.
// Real on a real corpus: [[entity-team]] named a skill directory, not a
// not-yet-written note, and staying silent about it (issue #67's own
// default for "resolves nowhere") told the reader nothing useful about a
// reference that was never going to resolve as written.
func detectBrokenLinks(home string, files []asset.MemoryFile, knownOtherAssets map[string]string) []Proposal {
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
			existsElsewhere := existsAnywhere[link]
			otherKind := knownOtherAssets[link]
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
			} else if !existsElsewhere && otherKind == "" {
				// Resolves nowhere at all, and isn't a known skill, agent or
				// plan either - a permitted forward reference (issue #67),
				// and looks identical on disk to real rot; the evidence to
				// tell them apart is intent, which the filesystem does not
				// carry.
				continue
			}
			subject := f.Store + "/" + f.Filename + " -> " + link
			// Excludes link == MemoryIndexSlug even if some asset literally
			// named "MEMORY" exists - that case is handled entirely by
			// brokenLinkText's isMemoryIndex branch, and letting this be
			// non-empty there would store evidence contradicting the
			// MEMORY-specific wording actually displayed (found by code
			// review, before this shipped).
			targetKind := ""
			if link != asset.MemoryIndexSlug && !existsElsewhere {
				// Checked after existsElsewhere, not before: a real memory
				// slug in another store is the more specific, more
				// actionable finding (issue #67's original case) even on
				// the rare chance another kind happens to share its name
				// (issues #66, #78).
				targetKind = otherKind
			}
			summary, rationale := brokenLinkText(f.Filename, link, link == asset.MemoryIndexSlug, targetKind)
			ev := map[string]any{
				"store": f.Store, "filename": f.Filename, "target_slug": link,
			}
			// One boolean per kind, set only when true, never a rename to a
			// shared "target_kind" field: target_is_skill already shipped
			// in #66 and is load-bearing for any ledger with a real
			// skill-shadow finding already stored - renaming it would
			// change that row's evidence hash the moment this shipped,
			// silently reviving any previously dismissed or applied one on
			// the very next pass (the same regression class #77 fixed,
			// found by code review before this one shipped). An
			// unconditional key of any shape has the identical problem for
			// the plain cross-store case, which is why none of the three
			// is ever written as a literal false either.
			switch targetKind {
			case asset.KindSkill:
				ev["target_is_skill"] = true
			case asset.KindAgent:
				ev["target_is_agent"] = true
			case asset.KindPlan:
				ev["target_is_plan"] = true
			}
			out = append(out, Proposal{
				Kind: KindBrokenLink, Subject: subject,
				Evidence:  ev,
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
