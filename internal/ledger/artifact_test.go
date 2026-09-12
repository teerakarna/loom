package ledger

import (
	"testing"
	"time"
)

func TestUpsertArtifactInsertsThenUpdates(t *testing.T) {
	db := openTestDB(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)

	rec := ArtifactRecord{Kind: "skill", Path: "/skills/a.md", Name: "a", Description: "does a"}
	if err := db.UpsertArtifact(rec, t0); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].FirstSeen != rows[0].LastSeen {
		t.Errorf("first insert: FirstSeen=%s LastSeen=%s, want equal", rows[0].FirstSeen, rows[0].LastSeen)
	}

	// Re-discovering the same artifact later should bump last_seen but never
	// first_seen, and refresh a changed description.
	rec.Description = "does a, updated"
	if err := db.UpsertArtifact(rec, t1); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows after re-upsert, want 1 (same path)", len(rows))
	}
	if rows[0].Description != "does a, updated" {
		t.Errorf("Description = %q, want updated value", rows[0].Description)
	}
	if rows[0].FirstSeen == rows[0].LastSeen {
		t.Errorf("FirstSeen should not equal LastSeen after a later re-upsert")
	}
}

func TestMarkStaleArtifacts(t *testing.T) {
	db := openTestDB(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(48 * time.Hour)
	cutoff := old.Add(24 * time.Hour)

	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/skills/old.md"}, old); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArtifact(ArtifactRecord{Kind: "skill", Path: "/skills/new.md"}, recent); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkStaleArtifacts(cutoff); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]ArtifactRow{}
	for _, r := range rows {
		byPath[r.Path] = r
	}
	if byPath["/skills/old.md"].Status != "stale" {
		t.Errorf("old artifact status = %q, want stale", byPath["/skills/old.md"].Status)
	}
	if byPath["/skills/new.md"].Status != "active" {
		t.Errorf("recent artifact status = %q, want active", byPath["/skills/new.md"].Status)
	}
}

func TestUpsertArtifactReactivatesStale(t *testing.T) {
	db := openTestDB(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := ArtifactRecord{Kind: "skill", Path: "/skills/a.md"}
	if err := db.UpsertArtifact(rec, t0); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkStaleArtifacts(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArtifact(rec, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "active" {
		t.Errorf("Status = %q, want active after re-discovery", rows[0].Status)
	}
}
