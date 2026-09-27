package selector

import (
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

func TestRecommendMatchesRelevantSkill(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "skill", Name: "github-unsigned-commit-merge-block", Path: "/skills/a.md",
			Description: "Diagnose a stuck PR blocked by an unsigned commit under required signatures branch protection"},
		{Kind: "skill", Name: "records-management", Path: "/skills/b.md",
			Description: "Manage Google Drive records and confirm writes before applying them"},
	}

	rec := Recommend(TaskDescriptor{Text: "PR merge is stuck, all checks green, blocked by unsigned commit"}, assets, nil)

	if len(rec.Matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(rec.Matches), rec.Matches)
	}
	if rec.Matches[0].Name != "github-unsigned-commit-merge-block" {
		t.Errorf("top match = %q, want the unsigned-commit skill", rec.Matches[0].Name)
	}
}

// TestRecommendMatchesCompoundAssetName is the regression test for issue
// #64's main finding: a real memory file named "devicefarm-public-devices-
// fail-device-gate" (one compound word, no separator between "device" and
// "farm") scored zero against the task "investigate why the iOS Device
// Farm @full leg fails", because "device"+"farm" (two words, from the
// query's own spacing) and "devicefarm" (one word, from the asset's own
// naming) were entirely different token strings. Confirmed on the AMC
// trial's real corpus: four real assets with "devicefarm" in the name,
// zero of them matched.
func TestRecommendMatchesCompoundAssetName(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "memory", Name: "devicefarm-public-devices-fail-device-gate", Path: "/mem/a.md",
			Description: "iOS fails on Device Farm's public devices at the gate check"},
		{Kind: "memory", Name: "unrelated-vendor-invoice-process", Path: "/mem/b.md",
			Description: "How to process a vendor invoice for payment"},
	}
	rec := Recommend(TaskDescriptor{Text: "investigate why the iOS Device Farm full leg fails and post the evidence"}, assets, nil)
	if len(rec.Matches) == 0 {
		t.Fatal("got no matches, want the devicefarm asset to surface")
	}
	if rec.Matches[0].Name != "devicefarm-public-devices-fail-device-gate" {
		t.Errorf("top match = %q, want the devicefarm asset", rec.Matches[0].Name)
	}
	for _, m := range rec.Matches {
		if m.Score > 1 {
			t.Errorf("%s: Score = %v, want <= 1", m.Name, m.Score)
		}
	}
}

// TestScoreNeverExceedsOne is the direct regression test for a bug code
// review found by reproduction, before this shipped: a candidate whose own
// description independently repeats the same phrase the query uses can
// match both the query's unigrams ("device", "farm") and their compound
// ("devicefarm") separately, so overlap exceeded the query's own token
// count and score returned 1.5 - breaking SkillMatch.Score's documented 0
// to 1 range. A caller treating that as a confidence fraction (the CLI's
// "[%.2f]", the MCP JSON score field) would see a nonsensical value.
func TestScoreNeverExceedsOne(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "memory", Name: "devicefarm-public-devices-fail-device-gate", Path: "/mem/a.md",
			Description: "iOS fails on Device Farm's public devices at the gate check"},
	}
	rec := Recommend(TaskDescriptor{Text: "device farm"}, assets, nil)
	if len(rec.Matches) == 0 {
		t.Fatal("got no matches, want the devicefarm asset to surface")
	}
	if rec.Matches[0].Score > 1 {
		t.Errorf("Score = %v, want <= 1 - this is the exact reproduction that found the bug", rec.Matches[0].Score)
	}
}

// TestScoreDoesNotFormCompoundAcrossFieldBoundary is the regression test for
// a bug code review found by reproduction: an earlier version joined
// a.Name+" "+a.Description into one string before compounding, so the last
// word of Name and the first word of Description could form a compound
// neither field actually contains. Name ends in "device", Description
// starts with "farm" - unrelated fields, no real "Device Farm" phrase
// anywhere in this asset - so a query for "Device Farm" must not treat this
// as a genuine compound match.
func TestScoreDoesNotFormCompoundAcrossFieldBoundary(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "memory", Name: "mobile testing device", Path: "/mem/a.md",
			Description: "farm health checks and unrelated invoice reconciliation steps for the finance team"},
	}
	rec := Recommend(TaskDescriptor{Text: "investigate the Device Farm outage today"}, assets, nil)
	if len(rec.Matches) == 0 {
		t.Fatal("got no matches - the real unigram overlap (device, farm) should still surface something")
	}
	// The true overlap is exactly "device" and "farm" as separate words,
	// not the boundary-artifact compound "devicefarm" too - measured at
	// 0.4 before this fix, 0.6 with the spurious boundary compound
	// counted as a third overlapping token.
	if rec.Matches[0].Score > 0.5 {
		t.Errorf("Score = %v, want ~0.4 (device+farm only) - a boundary compound between "+
			"unrelated Name/Description fields must not count as a real match", rec.Matches[0].Score)
	}
}

// TestRecommendFallsBackToWeakMatchesRatherThanNone is the regression test
// for issue #64's second finding: a query with some genuine but too-thin
// overlap to clear minScore returned nothing at all, even though the store
// held an asset a human would recognize as relevant. A weak, clearly
// labelled guess is strictly better than silence - the caller can discard
// it, but cannot discover an asset it was never told about.
func TestRecommendFallsBackToWeakMatchesRatherThanNone(t *testing.T) {
	assets := []ledger.AssetRow{
		// Deliberately thin overlap with the query below: shares only
		// "vendor", nothing else, against a long, wordy query - real
		// overlap, but not enough to clear minScore on its own.
		{Kind: "memory", Name: "vendor-onboarding-checklist", Path: "/mem/a.md",
			Description: "steps to onboard a new vendor into the procurement system"},
	}
	rec := Recommend(TaskDescriptor{
		Text: "chase outstanding invoices and confirm the vendor has been paid for last quarter's work",
	}, assets, nil)
	if len(rec.Matches) == 0 {
		t.Fatal("got no matches, want the weak-but-real vendor match surfaced instead of silence")
	}
	if !rec.BelowThreshold {
		t.Error("BelowThreshold = false, want true - this match didn't clear minScore on its own merits")
	}

	// A query with zero overlap at all must still return nothing - the
	// fallback is for weak-but-real matches, not a guarantee of always
	// returning something regardless of relevance.
	rec = Recommend(TaskDescriptor{Text: "refactor the payment gateway retry logic"}, assets, nil)
	if len(rec.Matches) != 0 || rec.BelowThreshold {
		t.Errorf("got %+v, want no matches and BelowThreshold=false for a genuinely unrelated query", rec)
	}
}

// TestRecommendSuppressesFlatTieBelowThreshold is the regression test for
// issue #87's second finding: five candidates from entirely unrelated
// domains tied at the exact same score (0.0833...) were still surfaced as
// "closest guesses", one of them a property-law skill against a debugging
// query. A five-way exact tie is what "the scorer found no signal at all"
// looks like on a 0-1 scale, not five weak-but-real guesses - unlike issue
// #64's own case, where the single weak match has no other candidate to tie
// against and is still shown.
func TestRecommendSuppressesFlatTieBelowThreshold(t *testing.T) {
	query := "chase outstanding invoices and confirm the vendor has been paid for last quarter's work"
	assets := []ledger.AssetRow{
		{Kind: "skill", Name: "unrelated-a", Path: "/skills/a.md", Description: "totally unrelated chase content"},
		{Kind: "skill", Name: "unrelated-b", Path: "/skills/b.md", Description: "totally unrelated paid content"},
		{Kind: "skill", Name: "unrelated-c", Path: "/skills/c.md", Description: "totally unrelated work content"},
	}
	rec := Recommend(TaskDescriptor{Text: query}, assets, nil)
	if len(rec.Matches) != 0 || rec.BelowThreshold {
		t.Errorf("got %+v, want no matches and BelowThreshold=false for an exact three-way tie", rec)
	}
}

func TestRecommendNoMatchBelowThreshold(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "skill", Name: "records-management", Path: "/skills/b.md", Description: "manage drive files"},
	}
	rec := Recommend(TaskDescriptor{Text: "refactor the payment gateway retry logic"}, assets, nil)
	if len(rec.Matches) != 0 {
		t.Errorf("got %d matches, want 0: %+v", len(rec.Matches), rec.Matches)
	}
}

func TestRecommendCapsAtMaxMatches(t *testing.T) {
	var assets []ledger.AssetRow
	for i := 0; i < 10; i++ {
		assets = append(assets, ledger.AssetRow{
			Kind: "skill", Name: "deploy-helper", Path: "/skills/x.md", Description: "deploy the service to production",
		})
	}
	rec := Recommend(TaskDescriptor{Text: "deploy the service to production"}, assets, nil)
	if len(rec.Matches) != maxMatches {
		t.Errorf("got %d matches, want %d (capped)", len(rec.Matches), maxMatches)
	}
}

func TestColdStartModelPlanning(t *testing.T) {
	model, effort, _ := coldStartModel("design the architecture and decide on the right tradeoff")
	if model != "opus" || effort != "high" {
		t.Errorf("got model=%s effort=%s, want opus/high", model, effort)
	}
}

func TestColdStartModelRetrieval(t *testing.T) {
	model, effort, _ := coldStartModel("find where this function is defined and list callers")
	if model != "haiku" || effort != "low" {
		t.Errorf("got model=%s effort=%s, want haiku/low", model, effort)
	}
}

func TestColdStartModelDefault(t *testing.T) {
	model, effort, _ := coldStartModel("implement the new feature")
	if model != "sonnet" || effort != "medium" {
		t.Errorf("got model=%s effort=%s, want sonnet/medium", model, effort)
	}
}

// TestColdStartModelWeakPlanningSignalDoesNotEscalate is the regression
// test for issue #63: the query "investigate why the iOS Device Farm @full
// leg fails and post the evidence on the Jira ticket" - a debugging task,
// not a planning one - matched a single incidental word ("why") and got
// escalated to opus/high. On real measured data, opus costs roughly 272x
// sonnet's per-run cost, so a single weak word is too little evidence for
// that jump - it now takes at least minPlanningHits distinct matches.
//
// Also guards issue #87's debugWords class: this same query now matches
// "investigate", and "why" was deliberately left out of debugWords rather
// than restored to it, exactly so this stays cheap - see debugWords' own
// comment.
func TestColdStartModelWeakPlanningSignalDoesNotEscalate(t *testing.T) {
	model, effort, _ := coldStartModel("investigate why the iOS Device Farm full leg fails and post the evidence on the Jira ticket")
	if model == "opus" || effort == "high" {
		t.Errorf("got model=%s effort=%s, want the cheaper direction - a debugging task with one incidental "+
			"word overlap must not escalate to the most expensive tier", model, effort)
	}
}

// TestColdStartModelSinglePlanningWordAloneIsNotEnough is the direct unit
// test for the minPlanningHits threshold: one planning-keyword match, with
// no competing retrieval signal, still defaults cheap rather than
// escalating - ambiguity defaults cheap, not expensive (issue #63).
func TestColdStartModelSinglePlanningWordAloneIsNotEnough(t *testing.T) {
	model, effort, _ := coldStartModel("decide what to have for lunch today")
	if model == "opus" || effort == "high" {
		t.Errorf("got model=%s effort=%s, want sonnet/medium - one planning word alone is too weak a signal", model, effort)
	}
}

// TestColdStartModelDebugEscalates is the regression test for issue #87's
// first finding: a bug of unknown cause was routed to haiku/low because
// "fix" matched retrievalWords and nothing matched planningWords - the
// opposite of what docs/design.md's "Cold start" section already says about
// ambiguous work being expensive to get wrong.
func TestColdStartModelDebugEscalates(t *testing.T) {
	model, effort, _ := coldStartModel(
		"Debug why the ingester extracts zero tokens from a 111 KB agent transcript and fix the parser.")
	if model != "opus" || effort != "high" {
		t.Errorf("got model=%s effort=%s, want opus/high - cause unknown, a wrong patch costs more than a slow one", model, effort)
	}
}

// TestColdStartModelSingleDebugWordAloneIsNotEnough mirrors
// TestColdStartModelSinglePlanningWordAloneIsNotEnough for debugWords: one
// incidental debugging word must not escalate alone, the same reasoning
// issue #63 established for planningWords.
func TestColdStartModelSingleDebugWordAloneIsNotEnough(t *testing.T) {
	model, effort, _ := coldStartModel("investigate the quarterly expenses report")
	if model == "opus" || effort == "high" {
		t.Errorf("got model=%s effort=%s, want sonnet/medium - one debugging word alone is too weak a signal", model, effort)
	}
}

// TestColdStartModelTieRationaleIsHonest is the regression test for a bug
// code review found by reproduction: a genuine tie between planning and
// retrieval hits (both sides clear their own bar) went cheap, correctly,
// but with a rationale claiming "matches retrieval/mechanical keywords" -
// which was false, since it matched exactly as many planning keywords.
// "review the design plan, then find and update the typo" matches
// planning={review, design, plan} and retrieval={find, update, typo}, 3
// each.
func TestColdStartModelTieRationaleIsHonest(t *testing.T) {
	model, effort, rationale := coldStartModel("review the design plan, then find and update the typo")
	if model != "haiku" || effort != "low" {
		t.Errorf("got model=%s effort=%s, want haiku/low - a tie still defaults cheap", model, effort)
	}
	if strings.Contains(rationale, "retrieval/mechanical keywords") {
		t.Errorf("Rationale = %q, want it to describe the tie honestly, not claim retrieval dominance it doesn't have", rationale)
	}
	if !strings.Contains(rationale, "equally") {
		t.Errorf("Rationale = %q, want it to say the signal was tied", rationale)
	}
}

func TestRecommendEmptyDescriptorNoMatches(t *testing.T) {
	assets := []ledger.AssetRow{{Kind: "skill", Name: "x", Description: "y"}}
	rec := Recommend(TaskDescriptor{Text: ""}, assets, nil)
	if len(rec.Matches) != 0 {
		t.Errorf("got %d matches for empty descriptor, want 0", len(rec.Matches))
	}
}

// TestRecommendPrefersEvidenceBackedPolicyOverColdStart is the regression
// test for issue #63's main finding: loom policy could show four agent
// types with evidence-backed rows, all landing on sonnet, while advise
// still reported "no history yet to personalize from" - the evidence
// existed, nothing consulted it. Text is deliberately planning-flavored
// (would cold-start to opus/high on its own) to prove the policy wins.
func TestRecommendPrefersEvidenceBackedPolicyOverColdStart(t *testing.T) {
	policy := &ledger.PolicyRow{
		AgentType: "Explore", Model: "haiku", Effort: "low", Source: "evidence", SampleSize: 68,
	}
	rec := Recommend(TaskDescriptor{Text: "design the architecture and decide on the right tradeoff"}, nil, policy)
	if rec.Model != "haiku" || rec.Effort != "low" {
		t.Errorf("got model=%s effort=%s, want the policy's haiku/low, not a cold-start guess", rec.Model, rec.Effort)
	}
	if !strings.Contains(rec.Rationale, "68") {
		t.Errorf("Rationale = %q, want it to cite the policy's sample size", rec.Rationale)
	}
}

// TestRecommendCitesHandSetPolicyDifferently confirms a hand-set policy
// (source="human") is still preferred over cold-start, with rationale text
// that doesn't claim measured evidence it doesn't have.
func TestRecommendCitesHandSetPolicyDifferently(t *testing.T) {
	policy := &ledger.PolicyRow{AgentType: "Plan", Model: "opus", Effort: "high", Source: "human"}
	rec := Recommend(TaskDescriptor{Text: "find and list every caller"}, nil, policy)
	if rec.Model != "opus" || rec.Effort != "high" {
		t.Errorf("got model=%s effort=%s, want the hand-set opus/high, not a cold-start guess", rec.Model, rec.Effort)
	}
	if strings.Contains(rec.Rationale, "measured") || strings.Contains(rec.Rationale, "evidence-backed") {
		t.Errorf("Rationale = %q, want it not to claim measured evidence for a hand-set policy", rec.Rationale)
	}
}

// TestRecommendFallsBackWhenNoPolicyExists confirms a nil policy (no
// agent type given, or none has a policy yet) leaves the cold-start
// heuristic completely unchanged.
func TestRecommendFallsBackWhenNoPolicyExists(t *testing.T) {
	rec := Recommend(TaskDescriptor{Text: "find where this function is defined and list callers"}, nil, nil)
	if rec.Model != "haiku" || rec.Effort != "low" {
		t.Errorf("got model=%s effort=%s, want the cold-start haiku/low", rec.Model, rec.Effort)
	}
}
