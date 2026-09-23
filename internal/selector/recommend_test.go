package selector

import (
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

	rec := Recommend(TaskDescriptor{Text: "PR merge is stuck, all checks green, blocked by unsigned commit"}, assets)

	if len(rec.Matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(rec.Matches), rec.Matches)
	}
	if rec.Matches[0].Name != "github-unsigned-commit-merge-block" {
		t.Errorf("top match = %q, want the unsigned-commit skill", rec.Matches[0].Name)
	}
}

func TestRecommendNoMatchBelowThreshold(t *testing.T) {
	assets := []ledger.AssetRow{
		{Kind: "skill", Name: "records-management", Path: "/skills/b.md", Description: "manage drive files"},
	}
	rec := Recommend(TaskDescriptor{Text: "refactor the payment gateway retry logic"}, assets)
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
	rec := Recommend(TaskDescriptor{Text: "deploy the service to production"}, assets)
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

func TestRecommendEmptyDescriptorNoMatches(t *testing.T) {
	assets := []ledger.AssetRow{{Kind: "skill", Name: "x", Description: "y"}}
	rec := Recommend(TaskDescriptor{Text: ""}, assets)
	if len(rec.Matches) != 0 {
		t.Errorf("got %d matches for empty descriptor, want 0", len(rec.Matches))
	}
}
