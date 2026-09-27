package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/teerakarna/loom/internal/ingest"
	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/policy"
)

// runStatus reports on Loom itself: what it knows, how current it is, and what
// it cannot answer. Deliberately read-only. `loom report` ingests as a side
// effect, so a user checking freshness with it would change the thing they
// were checking; this never writes.
func runStatus(args []string) error {
	root, err := defaultProjectsRoot()
	if err != nil {
		return err
	}
	// issue #94: shared parseArgs (cmd/loom/args.go), replacing this
	// command's own previously-nonexistent validation - any argument,
	// including an unrecognized flag, used to be assigned straight into
	// root with no check at all.
	positional, positionalGiven, _, err := parseArgs(args, "usage: loom status [path]", true, nil)
	if err != nil {
		return err
	}
	if positionalGiven {
		root = positional
	}
	// See runReport: ledger paths are stored resolved (issue #88, plus the
	// long-standing relative-vs-absolute reason), and every comparison
	// against them here is textual, so an unresolved root silently
	// misreports current transcripts as unseen and out-of-root at the same
	// time.
	root, err = resolveRoot(root)
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

	st, err := db.Status()
	if err != nil {
		return err
	}

	fmt.Printf("Ledger:    %s", ledgerPath)
	if fi, err := os.Stat(ledgerPath); err == nil {
		fmt.Printf("  (%.1f MB)", float64(fi.Size())/(1024*1024))
	}
	fmt.Println()
	fmt.Println("           machine-local by design; never sync or commit it (design constraint 12)")
	fmt.Println()

	fresh, err := freshness(db, root)
	if err != nil {
		// A failed scan costs the freshness block and nothing else. The ledger
		// contents, lanes and policy readiness below do not depend on the walk,
		// and returning here meant one directory vanishing mid-walk printed a
		// bare lstat error in place of the entire command's output. Say the scan
		// failed, then carry on with what is still answerable - this command's
		// own comment says naming what it cannot answer is part of the job.
		fmt.Println("Freshness:")
		fmt.Printf("  could not scan %s: %v\n", root, err)
		fmt.Println("  counts omitted rather than guessed; the ledger figures below are unaffected")
		fmt.Println()
		return statusRest(db, st)
	}
	fmt.Println("Freshness:")
	fmt.Printf("  transcripts on disk       %d\n", fresh.onDisk)
	fmt.Printf("  ingested and current      %d\n", fresh.current)
	if fresh.stale > 0 {
		fmt.Printf("  ingested but STALE        %d  (grown since; run `loom report`)\n", fresh.stale)
	} else {
		fmt.Printf("  ingested but stale        0\n")
	}
	// A separate bucket from stale, deliberately (issue #89): right after
	// CurrentFeatureVersion moves, every row here is unchanged in size and
	// still due for a re-read, and "grown since" would be a false reason for
	// a file that has not grown at all.
	if fresh.behindVersion > 0 {
		fmt.Printf("  ingested, needs re-read   %d  (loom upgraded; run `loom report`)\n", fresh.behindVersion)
	}
	if fresh.unseen > 0 {
		fmt.Printf("  never ingested            %d  (run `loom report`)\n", fresh.unseen)
	} else {
		fmt.Printf("  never ingested            0\n")
	}
	// Printed whenever non-zero so the five counts above still add up to
	// `transcripts on disk`. A file loom found but could not stat is neither
	// current, stale, behind-version, nor unseen, and silently dropping it
	// makes the arithmetic wrong with nothing on screen to explain the gap.
	// Names no cause. "unreadable" would guess at one, and since WalkDir had
	// just listed the entry the likeliest reason is that it went away in
	// between, not permissions - which is also why this must not share wording
	// with "in ledger, unreadable" a few lines down, a different state entirely.
	if fresh.unreadableOnDisk > 0 {
		fmt.Printf("  ingested, state unknown   %d  (walked, then could not be stat'd to compare size)\n", fresh.unreadableOnDisk)
	}
	// Label widths match deliberately: these counts are read against each
	// other, and a one-character shift makes them look like separate columns.
	if fresh.missing > 0 {
		fmt.Printf("  in ledger, gone from disk %d\n", fresh.missing)
	}
	if fresh.unreadable > 0 {
		fmt.Printf("  in ledger, unreadable     %d  (permissions or an unavailable mount, not data loss)\n", fresh.unreadable)
	}
	// No claim about the file here. This bucket is keyed on the path alone, so
	// the row is prunable whether the file is still on disk or not, and saying
	// "file still there" would be asserting a disk state nothing checked. It does
	// not name the two reasons either: "not a transcript" was accurate while the
	// journal was the only one, and became a false statement about a relative row
	// the moment that was added.
	//
	// "clears what it can replace" rather than "deletes these rows", because the
	// two classes are not cleared the same way: a journal goes outright, a relative
	// row goes only once the walk has produced its replacement. Against a narrowed
	// root a relative row is correctly counted here and correctly left alone by the
	// report, and the earlier unconditional wording promised otherwise.
	if fresh.prunable > 0 {
		fmt.Printf("  in ledger, unmatchable    %d  (`loom report` clears what it can replace)\n", fresh.prunable)
	}
	if fresh.outsideRoot > 0 {
		fmt.Printf("  in ledger, outside root   %d  (nothing to do; the root is an argument)\n", fresh.outsideRoot)
	}
	// The remainder, and the label says exactly that rather than naming a cause.
	// Everything above is keyed on a signal of its own; this one is what is left
	// when none of them fired, so the only honest line is a description of the
	// conditions that got it here. Claiming "not a path this version walks"
	// without calling the predicate that decides it is the same overreach the
	// three rounds before this one kept producing - and so was the version of this
	// line that said "readable, not outside the root": stat succeeding does not
	// mean the file can be opened, and outsideRoot answers false for anything it
	// cannot resolve as well as for "inside", so neither word was checked. Both are
	// now stated as what was actually established. That second hedge is about what
	// the predicate returns, not about a state reachable from here - every path
	// getting this far is absolute and cleaned, so the unresolvable case does not
	// arise. Wording a label to what its check establishes, rather than to what
	// happens to be reachable today, is what stops it going stale the next time the
	// caller changes.
	if fresh.unexplained > 0 {
		fmt.Printf("  in ledger, unexplained    %d  (exists, not known to be outside the root, and not walked - worth reporting)\n", fresh.unexplained)
	}
	fmt.Println()

	return statusRest(db, st)
}

// statusRest prints everything that does not depend on walking the projects
// root. Split out so a failed scan costs the freshness block alone rather than
// the whole command.
func statusRest(db *ledger.DB, st ledger.LedgerStatus) error {
	fmt.Println("Ledger contents:")
	fmt.Printf("  runs         %d (%d sessions, %d agents)\n", st.Runs, st.SessionRuns, st.AgentRuns)
	if st.Unattributed > 0 {
		fmt.Printf("               %d agent run(s) have no readable .meta.json, so no agent type\n", st.Unattributed)
	}
	fmt.Printf("  agent types  %d\n", st.AgentTypes)
	fmt.Printf("  lanes        %d\n", st.Lanes)
	if st.UnattributedLanes > 0 {
		fmt.Printf("               %d run(s) sit outside the projects root, so no lane\n", st.UnattributedLanes)
	}
	fmt.Printf("  assets       %d (%d stale)\n", st.Assets, st.StaleAsset)
	fmt.Printf("  events       %d\n", st.Events)
	fmt.Printf("  policies     %d deliberate\n", st.Policies)
	if st.EarliestRun != "" {
		fmt.Printf("  run window   %s to %s\n", st.EarliestRun, st.LatestRun)
	}
	fmt.Println()

	if summary, err := db.Report(); err == nil && len(summary.ByLane) > 1 {
		fmt.Println("Lanes seen:")
		for _, l := range summary.ByLane {
			fmt.Printf("  %-28s %3d run(s)\n", ingest.LaneDisplay(l.Lane), l.Runs)
		}
	}
	fmt.Println()

	stats, err := db.StatsByAgentType()
	if err != nil {
		return err
	}
	ready := 0
	for _, s := range stats {
		if s.Runs >= policy.MinSampleSize {
			ready++
		}
	}
	fmt.Println("Policy readiness:")
	fmt.Printf("  %d of %d agent type(s) have the %d runs needed before evidence displaces a default\n",
		ready, len(stats), policy.MinSampleSize)
	if ready == 0 && len(stats) > 0 {
		fmt.Println("  everything is resolving to a labelled shipped default, which is correct, not a fault")
	}
	fmt.Println()

	// Saying what this cannot answer is part of the job. A status command that
	// implies completeness it does not have is worse than none.
	fmt.Println("Not tracked yet:")
	fmt.Println("  - when each run was ingested (only when the run itself happened)")
	fmt.Println("  - which files failed to parse on the last pass (logged to stderr, then forgotten)")
	return nil
}

type freshnessCounts struct {
	onDisk, current, stale, unseen int
	// A row unchanged in size but stored under an older CurrentFeatureVersion
	// (issue #89): NeedsIngest re-reads it for the same reason it re-reads a
	// grown file, but the two are not the same fact about the file, and
	// folding this into stale would misreport the reason.
	behindVersion int
	// A file Walk returned that could not be stat'd, so its size cannot be
	// compared. Counted so current+stale+behindVersion+unseen+this equals
	// onDisk; a state that vanishes from a set of totals that are read
	// against each other is worse than one with an awkward name.
	unreadableOnDisk int
	// The five ways a ledger row can fail to come back from Walk. Four are
	// keyed on a positive signal; unexplained is the honest remainder, and is
	// labelled as one rather than given a cause it has not checked. See the
	// classifier in freshness.
	missing, unreadable, prunable, outsideRoot, unexplained int
}

// freshness compares the ledger against what is on disk right now, without
// ingesting. This is the check that would have surfaced the staleness bug:
// a transcript that has grown since it was read is reported here rather than
// silently under-counted in the next report.
func freshness(db *ledger.DB, root string) (freshnessCounts, error) {
	var f freshnessCounts

	known := map[string]ledger.KnownRun{}
	rows, err := db.KnownRuns()
	if err != nil {
		return f, err
	}
	for _, r := range rows {
		known[r.Path] = r
	}

	paths, err := ingest.Walk(root)
	if err != nil {
		// A projects root that does not exist at all is a normal state for a
		// new install, and the ledger rows are still worth classifying: nothing
		// was walked, so every one of them is genuinely not-walked and the loop
		// below is as accurate as it ever is.
		//
		// Every other walk failure is different in kind and must not take the
		// same path. WalkDir reports a directory removed mid-walk as an ENOENT
		// too, and there `paths` is partial or empty while the files are still
		// there: treating that as a new install reports live, current
		// transcripts as gone from disk. Which is why the question asked is
		// about the root and not about the error - an ENOENT alone cannot tell
		// the two apart.
		//
		// The first version of this block returned the zero struct here without
		// classifying anything, so a missing root printed "0 transcripts on
		// disk, 0 ingested" against a ledger holding hundreds of rows. Absence
		// read as confirmed-empty is the failure this project has already had
		// once, in the memory store.
		if !rootIsAbsent(root) {
			return f, err
		}
		paths = nil
	}

	onDisk := map[string]bool{}
	for _, p := range paths {
		onDisk[p] = true
		f.onDisk++
		r, ok := known[p]
		if !ok {
			f.unseen++
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			f.unreadableOnDisk++
			continue
		}
		// issue #96: the same predicate NeedsIngest keys re-ingestion on,
		// not an independently maintained copy of it.
		switch r.Freshness(fi.Size()) {
		case ledger.FreshnessStale:
			f.stale++
		case ledger.FreshnessNeedsReread:
			f.behindVersion++
		default:
			f.current++
		}
	}
	for p := range known {
		if onDisk[p] {
			continue
		}
		// "Walk did not return it" is five different states, and the whole
		// value of this block is telling them apart - each one implies a
		// different action, and two of them imply none at all. Four are keyed
		// on a positive signal of their own; the fifth is the remainder, and
		// the thing that matters is that its label claims nothing more than
		// "none of the above fired":
		//
		//   - not a path any walk can match, and `loom report` clears it: the
		//     one case where naming a remedy is honest, so it is keyed on the
		//     same predicate the prune uses. "Clears", not "deletes" - one of
		//     the two classes is deleted only once the walk has produced its
		//     replacement, and the label must not overstate the weaker half.
		//   - genuinely absent: the row outlived its file.
		//   - present but unreadable: says nothing about the data, and must
		//     not be reported as absence.
		//   - demonstrably outside the root being asked about: normal, nothing
		//     to do, and decided by comparing against the root rather than by
		//     exhaustion.
		//   - everything else: reported as unexplained, because it is.
		//
		// The out-of-root case is why this cannot be inferred from Walk's
		// silence at all: the root is an argument, and the ledger deliberately
		// holds runs from outside it (that is what an empty lane means).
		// Guessing from absence reported 221 real transcripts as prunable
		// against a narrowed root. The remainder bucket exists because a row
		// like <root>/sess/subagents/workflows/wf-1/other.jsonl is neither
		// prunable nor outside the root, and naming it either would be a false
		// statement about a row that never clears - but it does not get a
		// confident label of its own either, since nothing here has checked why
		// it is there.
		if isPrunableLedgerPath(p) {
			f.prunable++
			continue
		}
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				f.missing++
			} else {
				f.unreadable++
			}
			continue
		}
		if outsideRoot(root, p) {
			f.outsideRoot++
			continue
		}
		f.unexplained++
	}
	return f, nil
}

// rootIsAbsent decides which kind of walk failure this is, and deliberately asks
// about the root rather than about the error. WalkDir reports both "the root you
// gave me does not exist" and "a directory disappeared while I was reading it" as
// ENOENT, and the two need opposite handling: the first is a new install and every
// ledger row is genuinely not-walked, the second leaves a partial file list where
// classifying would report live transcripts as unexplained.
//
// Named rather than inlined because it is the only thing separating those two, and
// the difference is invisible from the error alone. The mid-walk variant cannot be
// staged deterministically in a test, so the predicate is tested directly instead.
func rootIsAbsent(root string) bool {
	_, err := os.Stat(root)
	return os.IsNotExist(err)
}

// outsideRoot reports whether p lies demonstrably outside root. A comparison that
// cannot be resolved at all is not an out-of-root answer and must not be reported
// as one: it is "cannot tell", so it falls through to the remainder bucket, which
// says so. Answering true here instead is how the relative-root bug printed
// "nothing to do; the root is an argument" about a current transcript.
//
// As called today that branch is unreachable, and saying so is the point rather
// than an excuse to drop it: filepath.Rel only fails when it cannot express p
// relative to root, which on this platform means one of them is relative, and the
// classifier buckets a relative row before it gets here while root is absolutised
// at both entry points. So this is a contract, not a live case - anything that
// cannot be resolved is "cannot tell", and only the caller's shape keeps that from
// happening.
//
// An earlier version of this comment offered differing volumes as the example,
// which is a counterexample to its own rule: two volumes cannot be Rel'd precisely
// because one is definitively outside the other. Nothing here runs on a platform
// with volume names, so the case is left out rather than half-handled. A later one
// said relative paths are "pruned before this is called", which was wrong in a way
// worth naming - they are *classified* before this is called, and a report only
// prunes the ones it can replace.
func outsideRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
