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
