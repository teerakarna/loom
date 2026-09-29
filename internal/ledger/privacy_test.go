package ledger

import (
	"strings"
	"testing"
	"time"

	"github.com/azva-co/loom/internal/ingest"
)

// TestNoContentStored is the check the design doc's "Verification" section
// requires and, until now, nothing in the repo actually ran: ingest a
// fixture containing a planted secret, then grep the database for it - it
// must not appear (docs/design.md, "Privacy by construction", constraint 6).
//
// Run through the real ingest -> ledger pipeline, including B7b's tables:
// tool_result content is exactly the kind of thing constraint 6 exists to
// keep out, since it is the most content-shaped data ingest ever sees. This
// is the test B7b's own verification section asks to still pass after the
// schema change - see docs/design.md, "Added for B7".
func TestNoContentStored(t *testing.T) {
	const plantedSecret = "NOT-A-REAL-SECRET-abcdefgh12345678"

	rs, err := ingest.IngestFile("../../testdata/synthetic-occupancy.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Sanity check the fixture actually contains the secret this test looks
	// for, or the rest of this test would pass for the wrong reason.
	var readBytes int64
	for _, e := range rs.ToolUsage {
		if e.ToolName == "Read" {
			readBytes = e.ResultBytes
		}
	}
	if readBytes != int64(len(plantedSecret)) {
		t.Fatalf("fixture drifted: Read.ResultBytes = %d, want len(plantedSecret) = %d",
			readBytes, len(plantedSecret))
	}

	db := openTestDB(t)
	path := "synthetic-occupancy.jsonl"
	if err := db.InsertRun(RunRecord{
		Path: path, SessionID: rs.SessionID, Kind: rs.Kind, Model: rs.Model,
		InputTokens: rs.Usage.InputTokens, OutputTokens: rs.Usage.OutputTokens,
		WeightedCost: rs.WeightedCost, ToolUseCount: rs.ToolUseCount,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := db.RunIDByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceToolUsage(id, rs.ToolUsage); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertCompactions(id, rs.Compactions); err != nil {
		t.Fatal(err)
	}

	// Also exercise B7a's asset_usage table (#39): its raw signals come
	// from tool_use input fields, but the fixture also plants both a skill
	// name and a file path in plain tool_result text (see
	// synthetic-asset-usage.jsonl) - the same shape a planted secret
	// would take if this join were ever built from message content instead
	// of structured fields.
	usageRS, err := ingest.IngestFile("../../testdata/synthetic-asset-usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAsset(AssetRecord{Kind: "skill", Path: "/skills/example-skill/SKILL.md", Name: "example-skill"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAsset(AssetRecord{Kind: "plan", Path: "/workspace/.claude/plans/my-plan.md", Name: "my-plan"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{Path: "synthetic-asset-usage.jsonl", SessionID: usageRS.SessionID, Kind: usageRS.Kind}); err != nil {
		t.Fatal(err)
	}
	usageID, err := db.RunIDByPath("synthetic-asset-usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := db.BuildAssetLookup()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceAssetUsage(usageID, lookup.Resolve(usageRS.SkillTouches, usageRS.FileTouches)); err != nil {
		t.Fatal(err)
	}

	assertNoSubstring(t, db, plantedSecret)
	// The decoy text itself, planted in the second fixture's Bash tool_result
	// specifically to prove it never gets stored anywhere as content.
	assertNoSubstring(t, db, "must not count as usage")
}

// assertNoSubstring walks every table's every row and column and fails if
// any TEXT/BLOB value contains needle. Generic over the schema (reads it
// from sqlite_master) so a new table added later is covered automatically,
// without this test needing to know its name.
func assertNoSubstring(t *testing.T, db *DB, needle string) {
	t.Helper()

	tables := func() []string {
		tableRows, err := db.sql.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tableRows.Close() }()
		var tables []string
		for tableRows.Next() {
			var name string
			if err := tableRows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, name)
		}
		if err := tableRows.Err(); err != nil {
			t.Fatal(err)
		}
		return tables
	}()

	for _, table := range tables {
		func() {
			rows, err := db.sql.Query(`SELECT * FROM ` + table)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Fatal(err)
				}
				for i, v := range vals {
					s, ok := v.(string)
					if !ok {
						if b, ok := v.([]byte); ok {
							s = string(b)
						} else {
							continue
						}
					}
					if strings.Contains(s, needle) {
						t.Errorf("%s.%s contains the planted secret: %q", table, cols[i], s)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		}()
	}
}
