package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
)

// defaultProjectsRoot is where Claude Code writes session transcripts.
func defaultProjectsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// defaultLedgerPath is where Loom's own SQLite ledger lives.
func defaultLedgerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".loom")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "loom.db"), nil
}

func runReport(args []string) error {
	root, err := defaultProjectsRoot()
	if err != nil {
		return err
	}
	// `loom report --lane <lane>` narrows to one project directory; a bare
	// argument is still the projects root, as before.
	var lane string
	for i := 0; i < len(args); i++ {
		if args[i] == "--lane" {
			if i+1 >= len(args) {
				return fmt.Errorf("--lane needs a value (see `loom report` for the lanes in your ledger)")
			}
			lane = args[i+1]
			i++
			continue
		}
		root = args[i]
	}
	// Absolute, always. The walked path is what gets stored as runs.path, so a
	// relative root writes relative rows, and every later comparison against
	// them is textual: `loom report .claude/projects` then `loom status
	// ~/.claude/projects` reported the same ingested, unchanged file as both
	// "never ingested" and "in ledger, outside root". Normalising at the two
	// entry points is what makes the ledger's paths comparable at all.
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}

	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// A lane is the project directory a transcript sits in, so it is only
	// meaningful relative to the projects root - never relative to whatever root
	// this particular invocation happened to walk. Resolved here rather than
	// inside ingestAll so the HOME lookup stays at the entry point and the tests
	// can drive ingestAll with a fixture root; a default that cannot be resolved
	// falls back to root, which is what the walk is being told to treat as the
	// corpus anyway.
	laneRoot := root
	if def, derr := defaultProjectsRoot(); derr == nil {
		if abs, aerr := filepath.Abs(def); aerr == nil {
			laneRoot = abs
		}
	}

	if err := ingestAll(db, root, laneRoot); err != nil {
		return err
	}

	summary, err := db.Report()
	if lane != "" {
		summary, err = db.ReportForLane(lane)
	}
	if err != nil {
		return err
	}
	if lane != "" {
		fmt.Printf("Lane:            %s\n", ingest.LaneDisplay(lane))
		if summary.TotalRuns == 0 {
			fmt.Println("No runs in that lane. `loom report` with no filter lists the lanes it knows.")
			return nil
		}
	}
	printReport(summary)
	return nil
}

// ingestAll walks root, ingests every transcript file not already in the
// ledger, and inserts one run per file. Reconciliation matching (attaching a
// session's reported <usage> figures to the corresponding agent run) happens
// after every file in the batch has been read, since a session file can
// reference an agent whose own file is discovered in any order during the
// walk.
// laneRoot is the projects root, which root may be narrower than: `loom report
// <root>/<one-project>` walks one project but its transcripts still belong to
// the lane that project directory names. Deriving the lane from root instead
// made a narrowed report rewrite lanes to whatever directory came first under
// it - measured on a real ledger, one narrowed report moved 82 of 85 runs off
// their project and onto two session UUIDs, and NeedsIngest then vetoed every
// later read that could have corrected them. Two parameters are not needed:
// laneRoot == root is exactly "this walk covers the whole corpus", which is what
// planRelativeSupersessions needs to know.
func ingestAll(db *ledger.DB, root string, laneRoot string) error {
	// Journals before the walk, deliberately. The prune reads the ledger and
	// touches no file, so making it wait behind a walk that can fail means the one
	// remedy `loom status` names for these rows does not run in the states where
	// the walk errors - including a projects root that does not exist, where status
	// names a remedy and report exits on an lstat before reaching this line.
	// Unconditional is safe here and only here: a journal was never a transcript,
	// so no walk will ever return it, nothing is waiting to replace it, and it owns
	// no child rows to lose.
	if err := pruneJournalRows(db); err != nil {
		return err
	}

	paths, err := ingest.Walk(root)
	if err != nil {
		return err
	}

	// The relative rows only now, once the walk has actually produced the files
	// that replace them - and planned here, committed further down once those
	// files have been read successfully. That ordering is the whole fix; see
	// supersedeRelativeRows for why "delete it, the walk will bring it back" was
	// three separate kinds of wrong when it ran above the walk.
	superseded, err := planRelativeSupersessions(db, paths, laneRoot == root)
	if err != nil {
		return err
	}

	// Size is what decides: a transcript that has grown since it was last
	// ingested must be re-read, or a live session's cost stays frozen at
	// whatever it was the first time loom looked (see ledger.NeedsIngest).
	sizes := map[string]int64{}
	var toInsert []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom: skipping %s: %v\n", p, err)
			continue
		}
		sizes[p] = fi.Size()
		needs, err := db.NeedsIngest(p, fi.Size())
		if err != nil {
			return err
		}
		// A planned supersession overrides NeedsIngest rather than being consulted
		// after it: the file whose relative row is about to go usually has an
		// absolute row of the same size already, which is exactly the case
		// NeedsIngest answers "no" to. Without the override the runs row survives
		// and its child rows never come back.
		if needs || superseded[p] != "" {
			toInsert = append(toInsert, p)
		}
	}
	if len(toInsert) == 0 {
		return nil
	}

	summaries := make(map[string]ingest.RunSummary, len(toInsert))
	for _, p := range toInsert {
		rs, err := ingest.IngestFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom: skipping %s: %v\n", p, err)
			continue
		}
		summaries[p] = rs
	}

	// Commit the supersessions only for files that actually read, and only now
	// that they have. The delete has to precede the inserts below - tool_use ids
	// and compaction uuids are globally unique and first-seen-wins, so the child
	// rows cannot be re-attached while the old row still holds them - but it must
	// not precede the read. Deleting first and finding out afterwards that the
	// file was unreadable or unparseable left the child rows gone with nothing
	// holding them and no way back: the surviving row's size already matched, so
	// NeedsIngest vetoed every later attempt. A file that fails to read simply
	// keeps its old relative row, which is a stale figure rather than no figure.
	if err := commitSupersessions(db, superseded, summaries); err != nil {
		return err
	}

	// Reconciliation figures reported by any session in this batch, keyed by
	// agent id, regardless of which session file reported them.
	reported := map[string]ingest.AgentUsage{}
	for _, rs := range summaries {
		for taskID, u := range rs.AgentReconciliations {
			reported[taskID] = u
		}
	}

	// Fetched once for the whole batch, not once per run: the assets
	// table does not change mid-batch, and it is small (design doc
	// "Ledger"). See ledger.BuildAssetLookup.
	lookup, err := db.BuildAssetLookup()
	if err != nil {
		return err
	}

	for path, rs := range summaries {
		rec := ledger.RunRecord{
			Path:                path,
			SizeBytes:           sizes[path],
			SessionID:           rs.SessionID,
			Kind:                rs.Kind,
			Model:               rs.Model,
			Lane:                ingest.LaneFromPath(laneRoot, path),
			AgentType:           rs.AgentType,
			Effort:              rs.Effort,
			StartedAt:           rs.StartedAt,
			EndedAt:             rs.EndedAt,
			InputTokens:         rs.Usage.InputTokens,
			OutputTokens:        rs.Usage.OutputTokens,
			CacheReadTokens:     rs.Usage.CacheReadInputTokens,
			CacheCreationTokens: rs.Usage.CacheCreationInputTokens,
			WeightedCost:        rs.WeightedCost,
			ToolUseCount:        rs.ToolUseCount,
			DenialCount:         rs.DenialCount,
			FeedbackCount:       rs.FeedbackCount,
		}
		if rs.Kind == "agent" {
			if id, ok := ingest.AgentIDFromPath(path); ok {
				if u, ok := reported[id]; ok {
					rec.ReportedSubagentTokens = ptr(u.SubagentTokens)
					rec.ReportedToolUses = ptr(u.ToolUses)
					rec.ReportedDurationMs = ptr(u.DurationMs)
				}
			}
		}
		if err := db.InsertRun(rec); err != nil {
			fmt.Fprintf(os.Stderr, "loom: failed to record %s: %v\n", path, err)
			continue
		}
		if err := recordOccupancyAndUsage(db, path, lookup, rs); err != nil {
			fmt.Fprintf(os.Stderr, "loom: failed to record occupancy/usage for %s: %v\n", path, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// recordOccupancyAndUsage writes rs's tool_usage, compaction, and
// asset_usage rows (B7b, B7a/#39). One RunIDByPath lookup shared across
// all three, rather than a separate lookup per table for a row InsertRun
// just wrote a moment earlier in this same loop iteration - found by code
// review as a redundant-query smell, not a correctness bug, but free to fix
// in the same pass.
func recordOccupancyAndUsage(db *ledger.DB, path string, lookup ledger.AssetLookup, rs ingest.RunSummary) error {
	runID, err := db.RunIDByPath(path)
	if err != nil {
		return err
	}
	if err := db.ReplaceToolUsage(runID, rs.ToolUsage); err != nil {
		return err
	}
	if err := db.InsertCompactions(runID, rs.Compactions); err != nil {
		return err
	}
	// A skill invoked under a name discovery has not seen, or a file outside
	// any known asset's path, resolves to nothing - this answers "was a
	// known asset used", not "what files exist". Requires discovery to
	// have found something at least once (an empty lookup resolves every
	// signal to nothing), the same precondition #38's staleness check
	// already has.
	touches := lookup.Resolve(rs.SkillTouches, rs.FileTouches)
	return db.ReplaceAssetUsage(runID, touches)
}

func printReport(s ledger.Summary) {
	fmt.Printf("Runs ingested:   %d (%d sessions, %d agents)\n", s.TotalRuns, s.SessionRuns, s.AgentRuns)
	fmt.Printf("Weighted cost:   %.0f (relative units - see docs/design.md, not real currency)\n", s.TotalWeightedCost)
	printCostByKind(s)
	fmt.Printf("Tool uses:       %d\n", s.TotalToolUses)
	fmt.Printf("Tool denials:    %d\n", s.TotalDenials)
	fmt.Printf("User feedback:   %d\n", s.TotalFeedback)

	// Concentration first: it changes how every breakdown below should be
	// read. If a handful of runs are nearly all the cost, the averages are
	// describing the tail, not the typical case.
	if len(s.Concentration) > 0 && s.TotalRuns > 1 {
		fmt.Println()
		fmt.Println("Cost concentration:")
		for _, c := range s.Concentration {
			if c.N >= s.TotalRuns {
				break
			}
			fmt.Printf("  top %-2d of %d runs   %5.1f%% of total\n", c.N, s.TotalRuns, c.Share*100)
		}
	}

	printGroup(s, "By model:", len(s.ByModel), func(i int) (string, int, float64, float64) {
		m := s.ByModel[i]
		return m.Model, m.Runs, m.WeightedCost, m.PerRun
	})

	if len(s.ByLane) > 1 {
		printGroup(s, "By lane:", len(s.ByLane), func(i int) (string, int, float64, float64) {
			l := s.ByLane[i]
			return ingest.LaneDisplay(l.Lane), l.Runs, l.WeightedCost, l.PerRun
		})
	}

	if len(s.ByAgentType) > 0 {
		printGroup(s, "By agent type:", len(s.ByAgentType), func(i int) (string, int, float64, float64) {
			a := s.ByAgentType[i]
			name := a.AgentType
			if name == "" {
				name = "(unattributed)"
			}
			return name, a.Runs, a.WeightedCost, a.PerRun
		})
	}

	if len(s.TopRuns) > 0 && s.TotalRuns > 1 {
		fmt.Println()
		fmt.Println("Most expensive runs:")
		for _, r := range s.TopRuns {
			label := r.Model
			if r.AgentType != "" {
				label = r.AgentType + " / " + r.Model
			}
			fmt.Printf("  %5.1f%%  %-30s %12.0f  %s\n", r.Share*100, label, r.WeightedCost, filepath.Base(r.Path))
		}
	}

	if s.AgentRuns > 0 {
		fmt.Println()
		fmt.Printf("%d/%d agent runs have a reported subagent_tokens figure from their parent's\n", s.UnreconciledAgents, s.AgentRuns)
		fmt.Println("task-notification. This is NOT reconciled against the weighted cost above -")
		fmt.Println("see docs/transcript-schema.md, \"Reconciliation does NOT hold\".")
	}
}

// printCostByKind shows what the headline figure is actually made of. A bare
// nine-digit total has no reference point, and hides the most useful fact
// about a corpus: spend dominated by cheap cached input and spend dominated by
// expensive fresh output look identical in the total and imply opposite
// actions (issue #9).
func printCostByKind(s ledger.Summary) {
	if len(s.CostByKind) == 0 || s.TotalWeightedCost == 0 {
		return
	}
	fmt.Println("  made up of:")
	for _, k := range s.CostByKind {
		if k.Tokens == 0 {
			continue
		}
		fmt.Printf("    %-12s %5.1f%%  %15d tokens x %.2f\n", k.Kind, k.Share*100, k.Tokens, weightFor(k.Kind))
	}
}

// weightFor is display-only, reading the same exported constants the cost
// function uses so the printed multiplier cannot drift from the real one.
func weightFor(kind string) float64 {
	switch kind {
	case "input":
		return ingest.WeightInput
	case "output":
		return ingest.WeightOutput
	case "cache read":
		return ingest.WeightCacheRead
	default:
		return ingest.WeightCacheWrite
	}
}

// printGroup renders one grouped breakdown with both the total and the
// per-run figure. Per-run is shown because totals alone conflate "costs more
// each time" with "used more often", and can invert the real ordering when
// run counts differ (issue #10).
func printGroup(s ledger.Summary, heading string, n int, row func(int) (string, int, float64, float64)) {
	if n == 0 {
		return
	}
	fmt.Println()
	fmt.Println(heading)
	fmt.Printf("  %-24s %6s %14s %14s\n", "", "runs", "total", "per run")
	for i := range n {
		name, runs, total, per := row(i)
		fmt.Printf("  %-24s %6d %14.0f %14.0f\n", name, runs, total, per)
	}
}

// pruneJournalRows drops rows for workflow orchestration journals. A ledger built
// before ingest.IsTranscript existed holds one run per
// `subagents/workflows/<id>/journal.jsonl`, with no model, no tokens and no tool
// calls. Nothing else removes them - reports, status and the MCP cost summary all
// count `runs` unfiltered, so they keep inflating the totals for as long as the
// ledger lives.
//
// Deliberately keyed on the one known-bad filename rather than on !IsTranscript,
// even though that reads as the more general fix. The complement of a whitelist
// has unbounded blast radius: if the host ever changes the subagent naming
// convention, or a ledger carries rows from a host that used a different one,
// every subagent run under subagents/ would be deleted with its child rows, and
// Walk would skip the same files so nothing would re-ingest them. A phantom row
// that survives is a wrong number; a wrongly deleted row is lost data. The paths
// go to stderr for the same reason - a count alone leaves no way to see what went.
//
// Keyed on the path shape rather than on cost, too: a genuinely zero-cost run
// (a subagent that produced no assistant turn) is real data and must survive.
func pruneJournalRows(db *ledger.DB) error {
	known, err := db.KnownRuns()
	if err != nil {
		return err
	}
	var journals []string
	for _, k := range known {
		if isJournalLedgerPath(k.Path) {
			journals = append(journals, k.Path)
		}
	}
	if len(journals) == 0 {
		return nil
	}
	deleted, err := db.DeleteRuns(journals)
	if err != nil {
		return err
	}
	reportPruned(deleted, "pruned unmatchable ledger row")
	return nil
}

// supersedeRelativeRows deletes ledger rows stored under a relative path - but
// only the ones this walk has demonstrably replaced - and returns the walked
// paths that must be re-read as a result.
//
// `loom report <relative root>` used to store whatever it walked, and paths are
// compared as text everywhere, so now that both entry points absolutise, such a
// row can never match a walked file again: it reads as unseen and unexplained at
// once, and the next report inserts a second row for the same file under its
// absolute name. It has to go, and it cannot be repaired in place instead - the
// working directory it was relative to is not recorded anywhere, and resolving it
// against whatever the cwd happens to be now is precisely the inference this
// command spent several rounds removing.
//
// What separates this from the journal prune is that the row is a full transcript
// with tool_usage, compaction and asset_usage rows hanging off it, so "delete it,
// the walk will bring it back" is an assumption and not a fact. An earlier version
// of this ran before the walk and stated that premise as though it were free.
// Measured, it was wrong three ways, and the worst of them is silent:
//
//   - Once one report has run since the root was absolutised, the ledger holds two
//     rows for the same file and every child row is still attached to the relative
//     one - tool_use_id and boundary_uuid are globally unique and insert with ON
//     CONFLICT DO NOTHING, so the second row's inserts were all no-ops. Deleting
//     the relative row takes those children with it, and NeedsIngest then vetoes
//     re-reading the file because the absolute row's size already matches. The
//     occupancy and asset-usage data is gone for good, across any number of later
//     reports, and nothing says so. Losing asset_usage is worse than losing a
//     number: propose.retireStaleAssets falls back to first_seen when an asset has
//     no usage row, so it starts offering live assets for retirement as never-used.
//   - A narrowed root (`loom report <root>/one-project`) deleted the relative rows
//     of every other project, which it was never going to walk or re-ingest.
//   - A root that is absent or on an unmounted volume deleted every relative row
//     and then failed on the lstat, leaving nothing to restore them from. That one
//     also broke a promise made two lines apart in `loom status`, which says an
//     unreadable row means "not data loss".
//
// So the delete needs a positive signal, and the signal is the walk itself: a
// walked path that ends in this row's path. Matched against the walk rather than
// against the filesystem because the walk is the thing that does the replacing,
// and a suffix is sound here - a relative row's text is whatever Walk handed back
// under a relative root, which is always a tail of that file's real absolute path.
// Ambiguity is left alone rather than guessed at: if two walked files could both be
// this row, nothing here can say which, so the row survives and `loom status` goes
// on reporting it.
//
// wholeCorpus is what makes that ambiguity check mean anything, and skipping it is
// a fourth way to lose the same data. The count is taken over the walk, so a walk
// that covers one project can see one candidate where the corpus holds two: given
// <root>/dup/sess.jsonl and <root>/old/dup/sess.jsonl, a relative row dup/sess.jsonl
// is correctly kept under the full root and would be confidently mismatched under
// `loom report <root>/old` - deleted, with its child rows, and its history credited
// to a file that already has a row of its own. A narrowed walk is not evidence about
// the corpus, so it does not get to supersede anything. It still walks and ingests
// normally; the migration is simply a full-root job.
//
// The matched paths are returned rather than deleted here, so the caller can read
// the replacements first and commit only the ones that read - see
// commitSupersessions. Both halves are needed: deleting without the forced re-read
// leaves the child rows gone, which is the first bullet above.
func planRelativeSupersessions(db *ledger.DB, walked []string, wholeCorpus bool) (map[string]string, error) {
	if !wholeCorpus {
		return nil, nil
	}
	known, err := db.KnownRuns()
	if err != nil {
		return nil, err
	}

	// Keyed on the replacement, valued on the row it replaces: the caller needs
	// the walked path to force a re-read, and the commit needs the row to delete.
	superseded := map[string]string{}
	for _, k := range known {
		if filepath.IsAbs(k.Path) {
			continue
		}
		replacement, ok := soleWalkedPathEndingIn(walked, k.Path)
		if !ok {
			continue
		}
		superseded[replacement] = k.Path
	}
	return superseded, nil
}

// commitSupersessions deletes the relative rows whose replacement actually read.
// Split from the planning half so the delete cannot outrun the read: see the
// caller for why it has to sit between the read and the inserts rather than
// before either.
func commitSupersessions(db *ledger.DB, superseded map[string]string, read map[string]ingest.RunSummary) error {
	var rows, replacements []string
	for replacement, row := range superseded {
		if _, ok := read[replacement]; !ok {
			continue
		}
		rows = append(rows, row)
		replacements = append(replacements, replacement)
	}
	if len(rows) == 0 {
		return nil
	}
	deleted, err := db.DeleteRuns(rows)
	if err != nil {
		return err
	}
	// Names both paths. An earlier version printed only the deleted relative row
	// next to the words "re-reading", so the line named the row that had just
	// stopped existing and never named the file being read - in output whose whole
	// purpose is making a change to the ledger explainable.
	gone := map[string]bool{}
	for _, p := range deleted {
		gone[p] = true
	}
	for i, row := range rows {
		if gone[row] {
			fmt.Fprintf(os.Stderr, "loom: replaced relative ledger row %s with %s, re-reading\n", row, replacements[i])
		}
	}
	return nil
}

// soleWalkedPathEndingIn finds the one walked path that rel is a tail of, and
// reports false when there is no such path or more than one. Zero means the walk
// cannot replace that row, so it must not be deleted; more than one means the row
// is genuinely ambiguous, and a coin toss between two real files is worse than
// leaving a row `loom status` already reports. The count is only as good as the
// walk it is taken over - see planRelativeSupersessions on wholeCorpus.
func soleWalkedPathEndingIn(walked []string, rel string) (string, bool) {
	suffix := string(filepath.Separator) + filepath.Clean(rel)
	var found string
	n := 0
	for _, p := range walked {
		if strings.HasSuffix(p, suffix) {
			found = p
			n++
		}
	}
	return found, n == 1
}

// reportPruned names what was deleted, not what was a candidate. DeleteRuns skips
// a path with no row rather than failing, so the two lists can differ, and
// printing the candidates claims prunes that did not happen - in output whose
// whole purpose is making a drop in the run count explainable.
//
// The reachable cause is a second `loom report` on the same ledger, and it
// involves no lock contention at all: KnownRuns runs outside any transaction,
// DeleteRuns opens its own, and a concurrent prune that commits in that window
// leaves our SELECT with no row to find. (An earlier version of this comment
// claimed the opposite - that such a race would surface as SQLITE_BUSY - and also
// offered a duplicate candidate path as the cause, which runs.path being UNIQUE
// makes impossible. Both wrong.)
func reportPruned(deleted []string, what string) {
	for _, p := range deleted {
		fmt.Fprintf(os.Stderr, "loom: %s %s\n", what, p)
	}
}

// isJournalLedgerPath reports whether a ledger row is a workflow orchestration
// journal. Not conditioned on the path being absolute: a relative journal row is
// doubly unmatchable, and letting the relative pass have it would leave it in the
// ledger forever, since no walk will ever produce the replacement that pass
// requires.
func isJournalLedgerPath(path string) bool {
	return filepath.Base(path) == "journal.jsonl" && !ingest.IsTranscript(path)
}

// isPrunableLedgerPath reports whether a ledger row's path is one no walk can
// match, in either of the two ways. Shared with `loom status`, which tells the
// user a report clears these rows: keying both on one predicate is what keeps
// that claim from drifting. It is not a hypothetical - status's first version
// inferred "not a transcript" from "Walk did not return it", which counted every
// row outside a narrowed root as prunable and pointed them at a remedy that would
// never touch them.
//
// What the two callers do with a true answer is deliberately not the same, and
// status's wording has to stay inside the weaker of the two. A journal is deleted
// outright; a relative row is deleted only once the walk has produced its
// replacement, so under a narrowed root a relative row can classify as prunable
// here and correctly survive the report. Both are still "no walk can match this",
// which is what this predicate answers and all that the label claims.
func isPrunableLedgerPath(path string) bool {
	return isJournalLedgerPath(path) || !filepath.IsAbs(path)
}
