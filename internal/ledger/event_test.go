package ledger

import (
	"testing"
	"time"
)

func TestInsertAndListEvents(t *testing.T) {
	db := openTestDB(t)
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := db.InsertEvent(ts, "s1", "outcome", map[string]string{"result": "accepted"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEvent(ts.Add(time.Minute), "", "outcome", map[string]string{"result": "rejected"}); err != nil {
		t.Fatal(err)
	}

	events, err := db.ListEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	// Most recent first.
	if events[0].SessionID != "" || events[0].Kind != "outcome" {
		t.Errorf("events[0] = %+v", events[0])
	}
	if events[1].SessionID != "s1" {
		t.Errorf("events[1].SessionID = %q, want s1", events[1].SessionID)
	}
}

func TestListProposalsEmptyByDefault(t *testing.T) {
	db := openTestDB(t)
	proposals, err := db.ListProposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 0 {
		t.Errorf("got %d proposals, want 0 (nothing writes to this table until B5)", len(proposals))
	}
}
