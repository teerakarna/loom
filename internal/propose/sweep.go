package propose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/teerakarna/loom/internal/asset"
	"github.com/teerakarna/loom/internal/ledger"
)

// Tier says how a pending proposal may be handled by `loom propose sweep`.
type Tier string

const (
	// TierSafe is a proposal whose fix loses nothing, and which checkSafe
	// verified against the files as they are right now. Loom still never
	// applies it: the sweep renders a script and the user runs it.
	TierSafe Tier = "safe"

	// TierDecide is everything else. It carries a bottom line and options
	// instead of a script.
	TierDecide Tier = "decide"
)

// SweepItem is one pending proposal after classification.
type SweepItem struct {
	ID      int64
	Kind    string
	Summary string
	Tier    Tier

	// Why is the reason this item is in its tier. For a decide item that
	// was a safe candidate, it names the check that failed.
	Why string

	// Script is the idempotent shell fix for a safe item, empty otherwise.
	Script string
	// Line is what a safe item adds, shown so the reader sees the exact edit.
	Line string

	// BottomLine and Options are set for decide items only.
	BottomLine string
	Options    []string
}

// safeName is the character set a filename or slug must stay inside to be
// written into MEMORY.md and into a shell script unescaped. It is the same
// set asset's own index regex accepts, so a line built from a name that
// passes is guaranteed to be seen as reachable on the next scan.
var safeName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Classify decides the tier of one pending proposal. It reads the files on
// disk at call time, not just the stored evidence: a proposal can be days old
// and the file may have changed since, so "safe" is only ever true of the
// state this call saw.
//
// The default is TierDecide. Only a kind that is named here and passes every
// check becomes safe, so a new proposal kind is never swept by accident.
func Classify(home string, row ledger.ProposalRow, ev map[string]any) SweepItem {
	it := SweepItem{
		ID: row.ID, Kind: row.Kind,
		Summary: SummaryFor(row.Kind, row.Subject, ev),
		Tier:    TierDecide,
	}
	if row.Kind == KindUnreachableAsset {
		line, script, why := checkUnreachableSafe(home, ev)
		if why == "" {
			it.Tier, it.Line, it.Script = TierSafe, line, script
			it.Why = "adds one line to MEMORY.md and changes nothing else"
			return it
		}
		it.Why = why
	}
	it.BottomLine, it.Options = adviceFor(row, ev)
	return it
}

// checkUnreachableSafe verifies that linking an unreachable memory file from
// its store's MEMORY.md can lose nothing, and returns the line and the script
// that add it. A non-empty why means it is not safe, and says which check
// failed.
func checkUnreachableSafe(home string, ev map[string]any) (line, script, why string) {
	store, _ := ev["store"].(string)
	filename, _ := ev["filename"].(string)
	if store == "" || filename == "" || store != filepath.Base(store) {
		return "", "", "the proposal does not name a store and file"
	}
	if !safeName.MatchString(filename) {
		return "", "", "the filename has characters that are not safe to write unescaped"
	}

	memDir := filepath.Join(home, ".claude", "projects", store, "memory")
	if !isRegularFile(filepath.Join(memDir, filename+".md")) {
		return "", "", "the memory file is gone or is not a regular file"
	}
	index := filepath.Join(memDir, "MEMORY.md")
	if !isRegularFile(index) {
		return "", "", "the store has no MEMORY.md, so adding one is creating a file, not a link"
	}
	if asset.MemoryIndex(home, store)[filename] {
		return "", "", "MEMORY.md already links it, the proposal is out of date"
	}

	title := filename
	for _, f := range memoryFilesIn(home, store) {
		if f.Filename == filename && safeName.MatchString(f.Slug) {
			title = f.Slug
		}
	}
	line = fmt.Sprintf("- [%s](%s.md)", title, filename)
	return line, indexAppendScript(index, filename+".md", line), ""
}

func memoryFilesIn(home, store string) []asset.MemoryFile {
	all, _, err := asset.DiscoverAllMemory(home)
	if err != nil {
		return nil
	}
	var out []asset.MemoryFile
	for _, f := range all {
		if f.Store == store {
			out = append(out, f)
		}
	}
	return out
}

// isRegularFile is false for a symlink, a directory and a missing path. A
// symlinked memory file or index could point anywhere, and a sweep that
// appends through one would write outside the store.
func isRegularFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// indexAppendScript is idempotent: it does nothing if the index already
// links the file, adds the missing newline if the index lacks a trailing one,
// and only ever appends, so every existing byte is kept.
func indexAppendScript(index, target, line string) string {
	q := shellQuote(index)
	return fmt.Sprintf(
		"grep -qF %s %s || { [ -z \"$(tail -c1 %s)\" ] || echo >> %s; echo %s >> %s; }",
		shellQuote("("+target+")"), q, q, q, shellQuote(line), q)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SweepScript joins the safe items into one script for a reader to review and
// run. It is empty when nothing is safe.
func SweepScript(items []SweepItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.Tier != TierSafe {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("#!/bin/sh\n# Generated by loom propose sweep. Review it, then run it. Loom does not.\nset -eu\n")
		}
		fmt.Fprintf(&b, "\n# #%d %s\n%s\n", it.ID, it.Summary, it.Script)
	}
	return b.String()
}

// adviceFor gives the bottom line and the options for a proposal that needs a
// decision. It states what the evidence shows and what each choice costs, and
// never recommends anything the evidence does not support.
func adviceFor(row ledger.ProposalRow, ev map[string]any) (string, []string) {
	switch row.Kind {
	case KindUnreachableAsset:
		return "Not linked from MEMORY.md, and the automatic fix did not pass its checks.",
			[]string{"link it by hand", "delete the file if it is no longer needed", "dismiss"}
	case KindFilenameSlugDrift:
		if strings.ReplaceAll(fmt.Sprint(ev["slug"]), "-", "_") == fmt.Sprint(ev["filename"]) ||
			strings.ReplaceAll(fmt.Sprint(ev["filename"]), "_", "-") == fmt.Sprint(ev["slug"]) {
			return "Only the separator differs. [[links]] resolve against the name, not the filename, so nothing is broken.",
				[]string{"dismiss, if you do not mind the mixed style", "rename the file, then fix its MEMORY.md link", "change name: to match the filename"}
		}
		return "The name and the filename say different things. [[links]] resolve against the name, so check which one is right.",
			[]string{"rename the file to match name:, then fix its MEMORY.md link", "change name: to match the file, then fix any [[links]] to it", "dismiss"}
	case KindBrokenLink:
		return "A [[link]] points at nothing. Whoever follows it gets no answer.",
			[]string{"fix the link to the right name", "create the missing target", "remove the link", "dismiss"}
	case KindPromoteMemoryDuplicate:
		return "The same note is copied across several stores, so it is a cross-project fact kept per project.",
			[]string{"promote it to a skill, which loads in every session", "move it to a shared reference doc that loads nothing until opened", "keep one copy and delete the rest", "dismiss"}
	case KindRetireAsset:
		return "Unused for a long time. Removing it touches your files and loom will not do it.",
			[]string{"delete it", "archive it somewhere outside the load path", "dismiss, if it is rarely but really used"}
	case KindPinModel:
		return "Records the model already in use as loom's deliberate policy for this agent type. It changes what loom recommends, not what runs.",
			[]string{fmt.Sprintf("`loom propose apply %d`, undone by `loom policy unset <agent-type>`", row.ID), "wait for more runs", "dismiss"}
	case KindRevertPolicy:
		return "A pinned policy got measurably worse after it was applied. Reverting restores the state you had evidence for.",
			[]string{fmt.Sprintf("`loom propose apply %d` to revert", row.ID), "keep the policy and dismiss", "wait for more runs"}
	default:
		return "No guidance written for this kind of proposal yet.",
			[]string{"read the evidence in `loom propose`", "dismiss"}
	}
}
