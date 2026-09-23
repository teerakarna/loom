package main

import (
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/propose"
)

// TestFilterPendingByLane is the regression test for issue #68: the four
// memory-finding kinds are store-scoped and should narrow to one lane, but
// pin_model/revert_policy/retire_asset are not - the policy they write is
// machine-global, so filtering them by lane would misrepresent what
// applying one actually does.
func TestFilterPendingByLane(t *testing.T) {
	pending := []ledger.ProposalRow{
		{ID: 1, Kind: propose.KindBrokenLink, Evidence: `{"store":"lane-a","filename":"x"}`},
		{ID: 2, Kind: propose.KindBrokenLink, Evidence: `{"store":"lane-b","filename":"y"}`},
		{ID: 3, Kind: propose.KindUnreachableAsset, Evidence: `{"store":"lane-a","filename":"z"}`},
		{ID: 4, Kind: propose.KindPinModel, Evidence: `{"agent_type":"Explore"}`},
		{ID: 5, Kind: propose.KindRetireAsset, Evidence: `{"path":"/s/old.md"}`},
	}

	got := filterPendingByLane(pending, "lane-a")

	ids := map[int64]bool{}
	for _, p := range got {
		ids[p.ID] = true
	}
	if !ids[1] || ids[2] || !ids[3] || !ids[4] || !ids[5] {
		t.Errorf("got ids %v, want {1,3,4,5} - lane-a's own findings plus every non-lane-scoped kind, "+
			"not lane-b's broken_link", ids)
	}
}

// TestFilterPendingByLaneUnknownLaneKeepsOnlyUnscoped confirms a lane that
// matches nothing still leaves the non-lane-scoped kinds visible, rather
// than an empty result silently implying the machine-global policy
// proposals were lane-scoped too.
func TestFilterPendingByLaneUnknownLaneKeepsOnlyUnscoped(t *testing.T) {
	pending := []ledger.ProposalRow{
		{ID: 1, Kind: propose.KindBrokenLink, Evidence: `{"store":"lane-a","filename":"x"}`},
		{ID: 2, Kind: propose.KindRevertPolicy, Evidence: `{"agent_type":"Explore"}`},
	}
	got := filterPendingByLane(pending, "no-such-lane")
	if len(got) != 1 || got[0].ID != 2 {
		t.Errorf("got %+v, want only the revert_policy proposal", got)
	}
}
