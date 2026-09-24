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

// writeSkillFile writes a subdirectory-convention skill (name/SKILL.md)
// under <home>/.claude/skills, the shape DiscoverGlobalSkills reads.
func writeSkillFile(t *testing.T, home, name, content string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeAgentFile writes a flat-file-convention agent (name.md) under
// <home>/.claude/agents - the shape DiscoverGlobalAgents also reads,
// unlike skills (scanSkillDir routes a flat file to KindReference instead).
func writeAgentFile(t *testing.T, home, name, content string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "agents", name+".md")
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
	got := detectBrokenLinks(home, files, nil)
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
	if got := detectBrokenLinks(home, files, nil); len(got) != 0 {
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
	got := detectBrokenLinks(home, files, nil)
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
	got := detectBrokenLinks(home, files, nil)
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
	if got := detectBrokenLinks(home, files, nil); len(got) != 0 {
		t.Errorf("got %+v, want no broken-link proposal - the target exists nowhere yet, a permitted forward reference", got)
	}

	// The target shows up later, in a different store - now it's a real,
	// actionable cross-store scoping problem, not a forward reference.
	writeMemoryFile(t, home, "store-b", "not-written-yet.md", "---\nname: not-written-yet\n---\nNow it exists, elsewhere.")
	files, _, err = asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got := detectBrokenLinks(home, files, nil)
	if len(got) != 1 || got[0].Evidence["target_slug"] != "not-written-yet" {
		t.Errorf("got %+v, want one broken link now that the target exists (in the wrong store)", got)
	}
}

// TestDetectBrokenLinks_SkillShadowedLinkIsFlagged is the regression test
// for issue #66: a [[link]] that resolves nowhere as a memory would
// normally be a permitted forward reference (issue #67), silently skipped.
// But when the name is a known skill instead, it will never become a
// memory - a [[link]] only ever resolves against a memory's own
// frontmatter name - so staying silent tells the reader nothing useful
// about a reference that was never going to resolve as written. Real on a
// real corpus: [[entity-team]] named a skill directory, not a
// not-yet-written note.
func TestDetectBrokenLinks_SkillShadowedLinkIsFlagged(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[entity-team]] for how the team works.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	knownOtherAssets := map[string]string{"entity-team": asset.KindSkill}
	got := detectBrokenLinks(home, files, knownOtherAssets)
	if len(got) != 1 || got[0].Evidence["target_slug"] != "entity-team" {
		t.Fatalf("got %+v, want one broken link flagging entity-team", got)
	}
	if !strings.Contains(got[0].Summary, "skill, not a memory") {
		t.Errorf("Summary = %q, want it to say this is a skill, not a memory", got[0].Summary)
	}
	if !strings.Contains(got[0].Rationale, "never a skill") {
		t.Errorf("Rationale = %q, want it to explain [[links]] never resolve against a skill", got[0].Rationale)
	}
	// Stored, not just carried in the in-process strings above - SummaryFor
	// rebuilds text from evidence alone at display time and has no other
	// way to tell this case apart from the cross-store one (found by code
	// review, before this shipped).
	if got[0].Evidence["target_is_skill"] != true {
		t.Errorf("Evidence = %+v, want target_is_skill = true", got[0].Evidence)
	}
}

// TestDetectBrokenLinks_CrossStoreTakesPriorityOverSkillMatch checks the
// deliberate tie-break when a link's target both exists as a real memory
// slug in a different store and happens to share a skill's name: the
// cross-store finding (issue #67's original case) wins, since it is the
// more specific, more actionable one - fix the reference, don't just note
// what the name also happens to be.
func TestDetectBrokenLinks_CrossStoreTakesPriorityOverSkillMatch(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[shared-name]].")
	writeMemoryFile(t, home, "store-b", "shared-name.md", "---\nname: shared-name\n---\nA real memory, elsewhere.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	knownOtherAssets := map[string]string{"shared-name": asset.KindSkill}
	got := detectBrokenLinks(home, files, knownOtherAssets)
	if len(got) != 1 {
		t.Fatalf("got %+v, want exactly one broken link", got)
	}
	if strings.Contains(got[0].Summary, "skill, not a memory") {
		t.Errorf("Summary = %q, want the cross-store wording, not the skill-shadow wording", got[0].Summary)
	}
	if !strings.Contains(got[0].Summary, "exists but not in this store") {
		t.Errorf("Summary = %q, want the cross-store wording to win", got[0].Summary)
	}
	// Absent, not a literal false - an unconditional key on every
	// cross-store finding would change its evidence hash for every
	// existing row the moment this shipped, silently reviving any
	// previously dismissed or applied one (see propose.go, Store's doc
	// comment on the same principle).
	if _, present := got[0].Evidence["target_is_skill"]; present {
		t.Errorf("Evidence = %+v, want target_is_skill absent - the cross-store case won", got[0].Evidence)
	}
}

// TestDetectBrokenLinks_AgentAndPlanShadowedLinksAreFlagged is the
// regression test for issue #78, generalizing #66's skill-only case: a
// [[link]] naming a real agent or plan is flagged the same way a
// skill-shadowed one is, with wording naming the actual kind.
func TestDetectBrokenLinks_AgentAndPlanShadowedLinksAreFlagged(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md",
		"---\nname: a\n---\nSee [[deploy-bot]] and [[migration-plan]] for context.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	knownOtherAssets := map[string]string{"deploy-bot": asset.KindAgent, "migration-plan": asset.KindPlan}
	got := detectBrokenLinks(home, files, knownOtherAssets)
	if len(got) != 2 {
		t.Fatalf("got %+v, want two broken links (deploy-bot, migration-plan)", got)
	}
	byTarget := map[string]Proposal{}
	for _, p := range got {
		byTarget[p.Evidence["target_slug"].(string)] = p
	}
	agent := byTarget["deploy-bot"]
	if !strings.Contains(agent.Summary, "which is an agent, not a memory") {
		t.Errorf("agent Summary = %q, want it to say this is an agent", agent.Summary)
	}
	if agent.Evidence["target_is_agent"] != true {
		t.Errorf("agent Evidence = %+v, want target_is_agent = true", agent.Evidence)
	}
	plan := byTarget["migration-plan"]
	if !strings.Contains(plan.Summary, "which is a plan, not a memory") {
		t.Errorf("plan Summary = %q, want it to say this is a plan", plan.Summary)
	}
	if plan.Evidence["target_is_plan"] != true {
		t.Errorf("plan Evidence = %+v, want target_is_plan = true", plan.Evidence)
	}
}

// TestGenerateMemoryFindings_WiresGlobalAgentsAndPlansIntoBrokenLinkCheck is
// the end-to-end counterpart: GenerateMemoryFindings itself discovers
// <home>/.claude/agents and <home>/.claude/plans and threads them through,
// not just detectBrokenLinks called directly. Also exercises the flat
// "name.md" convention, which skills never use (scanSkillDir routes it to
// KindReference instead) but agents and plans do via scanMarkdownDir.
func TestGenerateMemoryFindings_WiresGlobalAgentsAndPlansIntoBrokenLinkCheck(t *testing.T) {
	home := t.TempDir()
	writeAgentFile(t, home, "deploy-bot", "---\nname: deploy-bot\n---\nBody.")
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[deploy-bot]] for context.")

	got, _, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		if p.Kind == KindBrokenLink && p.Evidence["target_slug"] == "deploy-bot" {
			found = true
			if !strings.Contains(p.Summary, "which is an agent, not a memory") {
				t.Errorf("Summary = %q, want the agent-shadow wording", p.Summary)
			}
		}
	}
	if !found {
		t.Errorf("got %+v, want a broken_link proposal for deploy-bot", got)
	}
}

// TestSummaryFor_LegacyTargetIsSkillEvidenceStillResolves confirms a
// broken_link row stored before #78 shipped - carrying only
// target_is_skill, the shape #66 alone ever wrote - still renders with the
// skill-shadow wording, not a blank or wrong classification. SummaryFor
// reconstructs otherKind from whichever of the three booleans is present,
// so this predates target_is_agent/target_is_plan existing at all and must
// still work.
func TestSummaryFor_LegacyTargetIsSkillEvidenceStillResolves(t *testing.T) {
	ev := map[string]any{"filename": "a", "target_slug": "entity-team", "target_is_skill": true}
	got := SummaryFor(KindBrokenLink, "store-a/a", ev)
	want := "a links to [[entity-team]], which is a skill, not a memory"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestGenerateMemoryFindings_WiresGlobalSkillsIntoBrokenLinkCheck is the
// end-to-end counterpart: GenerateMemoryFindings itself discovers
// <home>/.claude/skills and threads it through, not just detectBrokenLinks
// called directly.
func TestGenerateMemoryFindings_WiresGlobalSkillsIntoBrokenLinkCheck(t *testing.T) {
	home := t.TempDir()
	writeSkillFile(t, home, "entity-team", "---\nname: entity-team\n---\nBody.")
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[entity-team]] for how the team works.")

	got, _, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		if p.Kind == KindBrokenLink && p.Evidence["target_slug"] == "entity-team" {
			found = true
			if !strings.Contains(p.Summary, "skill, not a memory") {
				t.Errorf("Summary = %q, want the skill-shadow wording", p.Summary)
			}
		}
	}
	if !found {
		t.Errorf("got %+v, want a broken_link proposal for entity-team", got)
	}
}

// TestDetectBrokenLinks_MatchesDirectoryNameOverDriftedFrontmatter is the
// regression test for a code-review finding on the first version of this
// fix: knownSkills was keyed only by assetFromFile's resolved Name
// (frontmatter preferred, else directory basename), but a [[link]] author
// references what they actually invoke the skill as - the directory name -
// which can drift from its own frontmatter (a real, documented failure
// mode on this exact codebase's history). Without matching the directory
// name too, a drifted skill would fall through to the silent
// forward-reference branch, reproducing the exact silence issue #66 exists
// to fix, for the specific drift case most likely to occur.
func TestDetectBrokenLinks_MatchesDirectoryNameOverDriftedFrontmatter(t *testing.T) {
	home := t.TempDir()
	// Directory is "entity-team", but its own frontmatter has drifted to
	// something else - the link names the directory, not the frontmatter.
	writeSkillFile(t, home, "entity-team", "---\nname: entity-team-v2\n---\nBody.")
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[entity-team]] for how the team works.")

	got, _, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		if p.Kind == KindBrokenLink && p.Evidence["target_slug"] == "entity-team" {
			found = true
		}
	}
	if !found {
		t.Errorf("got %+v, want entity-team still flagged via its directory name despite drifted frontmatter", got)
	}
}

// TestDetectBrokenLinks_ReferenceFileIsNotCalledASkill is the regression
// test for a code-review finding: a flat .md file directly under
// ~/.claude/skills/ is never loadable as a skill (scanSkillDir's own doc
// comment, KindReference) - calling one "a skill" in the rationale text
// would be factually wrong. A link naming a reference file's name must
// fall through to the ordinary silent forward-reference case, not the
// skill-shadow wording.
func TestDetectBrokenLinks_ReferenceFileIsNotCalledASkill(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "skills", "some-notes.md"), []byte("# Some Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[some-notes]] for background.")

	got, _, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.Kind == KindBrokenLink && p.Evidence["target_slug"] == "some-notes" {
			t.Errorf("got a broken_link proposal for a reference file: %+v, want silence (permitted forward reference)", p)
		}
	}
}

// TestDetectBrokenLinks_EvidenceHashStableForCrossStoreFindings is the
// regression test for the most severe finding from code review on the
// first version of this fix: target_is_skill was added to every
// KindBrokenLink Evidence map unconditionally (true or false), which
// changes Proposal.Hash() for every plain cross-store finding too, not
// just skill-shadow ones. UpsertProposal resets status to pending on any
// evidence-hash change regardless of the row's prior status (dismissed or
// applied included) - so on the first pass after that version shipped,
// every previously dismissed or applied cross-store broken_link proposal
// on any real ledger would have silently reverted to pending, discarding
// the user's earlier decision. Confirmed by hashing the two proposals a
// before/after-this-fix corpus would produce for the identical cross-store
// finding and checking they match.
func TestDetectBrokenLinks_EvidenceHashStableForCrossStoreFindings(t *testing.T) {
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "a.md", "---\nname: a\n---\nSee [[shared-name]].")
	writeMemoryFile(t, home, "store-b", "shared-name.md", "---\nname: shared-name\n---\nA real memory, elsewhere.")

	files, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	// One run with a populated knownSkills set that happens not to match
	// this link's target, one with none at all - both are the same
	// cross-store finding and must hash identically, or a ledger that
	// picks up a new, unrelated skill on disk would revive every existing
	// dismissed/applied broken_link proposal alongside it.
	withSkills := detectBrokenLinks(home, files, map[string]string{"unrelated-skill": asset.KindSkill})
	withoutSkills := detectBrokenLinks(home, files, nil)
	if len(withSkills) != 1 || len(withoutSkills) != 1 {
		t.Fatalf("got %d and %d proposals, want exactly one each", len(withSkills), len(withoutSkills))
	}
	if withSkills[0].Hash() != withoutSkills[0].Hash() {
		t.Errorf("Hash() differs (%q vs %q) for the identical cross-store finding - an unrelated skill on "+
			"disk must not change this proposal's evidence hash", withSkills[0].Hash(), withoutSkills[0].Hash())
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
	if len(scanned.Present) != 0 || len(scanned.Scanned) != 0 {
		t.Errorf("scanned = %+v, want a zero value - a missing home is not a scan failure", scanned)
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
	if !scanned.Scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a scanned - its memory directory is readable this pass", scanned)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
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
	if !scanned.Present["store-a"] {
		t.Fatalf("scanned = %+v, want store-a still present as a project directory", scanned)
	}
	if scanned.Scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a not scanned this pass - its memory directory is unreadable", scanned)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
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
	if _, err := Store(db, findings, &scanned, now); err != nil {
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
	if !scanned.Scanned["store-a"] {
		t.Fatalf("scanned = %+v, want store-a scanned - it was readable this pass", scanned)
	}
	if len(findings) != 0 {
		t.Fatalf("got %+v, want none - the file is linked now", findings)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
		t.Fatal(err)
	}

	if pending, _ := db.ListProposals(true); len(pending) != 0 {
		t.Errorf("got %+v, want none - store-a was fully scanned and the finding is genuinely gone", pending)
	}
}

// TestStoreWithdrawsWhenAStoreIsPermanentlyDeleted is the regression test
// for the second of two findings from a second code-review round on this
// fix: the first version's protection had no way to tell "transiently
// unreadable" apart from "gone for good", so a genuinely deleted store's
// stale proposals could never withdraw again - stuck pending forever,
// defeating #40's whole purpose in a new way.
func TestStoreWithdrawsWhenAStoreIsPermanentlyDeleted(t *testing.T) {
	db := openDB(t)
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNo MEMORY.md links this.")

	findings, scanned, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
		t.Fatal(err)
	}
	if pending, _ := db.ListProposals(true); len(pending) != 1 {
		t.Fatalf("got %+v, want exactly one proposal after the first pass", pending)
	}

	// The whole project is gone, not just its memory/ subdirectory -
	// removed, not merely unreadable.
	if err := os.RemoveAll(filepath.Join(home, ".claude", "projects", "store-a")); err != nil {
		t.Fatal(err)
	}

	findings, scanned, err = GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if scanned.Present["store-a"] {
		t.Fatalf("scanned = %+v, want store-a absent from Present - it no longer exists as a project", scanned)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
		t.Fatal(err)
	}

	if pending, _ := db.ListProposals(true); len(pending) != 0 {
		t.Errorf("got %+v, want the proposal withdrawn - store-a is genuinely gone, not merely unreadable", pending)
	}
}

// TestStoreProtectsEverythingWhenTheRootScanFails is the regression test
// for the first of two findings from that same review round: the first
// version only protected a single unreadable store, but treated a
// missing/unreadable <home>/.claude/projects root itself as "confirmed
// empty, nothing to protect" - which would mass-withdraw every real,
// unchanged lane-scoped proposal across every store if the root itself
// went missing for one pass (a wrong $HOME, a mount hiccup), reproducing
// issue #59's exact failure mode one directory level up.
func TestStoreProtectsEverythingWhenTheRootScanFails(t *testing.T) {
	db := openDB(t)
	home := t.TempDir()
	writeMemoryFile(t, home, "store-a", "orphan.md", "---\nname: orphan\n---\nNo MEMORY.md links this.")

	findings, scanned, err := GenerateMemoryFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
		t.Fatal(err)
	}
	if pending, _ := db.ListProposals(true); len(pending) != 1 {
		t.Fatalf("got %+v, want exactly one proposal after the first pass", pending)
	}

	// The same ledger, but this one invocation resolves a different, empty
	// root - the class of failure DiscoverAllMemory tolerates as "no
	// stores" rather than an error (a wrong $HOME for one call, a mount
	// hiccup, or a genuinely fresh machine all look identical to it).
	wrongHome := t.TempDir()
	findings, scanned, err = GenerateMemoryFindings(wrongHome)
	if err != nil {
		t.Fatal(err)
	}
	if scanned.Present != nil {
		t.Fatalf("scanned = %+v, want a nil Present - the root itself was never enumerated", scanned)
	}
	if len(findings) != 0 {
		t.Fatalf("got %+v, want none - nothing exists under the wrong root", findings)
	}
	if _, err := Store(db, findings, &scanned, now); err != nil {
		t.Fatal(err)
	}

	if pending, _ := db.ListProposals(true); len(pending) != 1 {
		t.Errorf("got %+v, want the proposal still pending - the root scan itself failed, so nothing said "+
			"any store's finding actually stopped being true", pending)
	}
}
