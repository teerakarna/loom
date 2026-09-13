package ingest

import (
	"path/filepath"
	"strings"
)

// LaneFromPath returns the lane a transcript belongs to: the project directory
// its session ran in, which is the first path segment under root.
//
// Both shapes resolve to the same lane, which is the point - a subagent
// belongs to the lane its parent session ran in:
//
//	<root>/-Users-bertie-projects-personal-dotfiles/<session>.jsonl
//	<root>/-Users-bertie-projects-personal-dotfiles/<session>/subagents/agent-<id>.jsonl
//
// This is a *description* of where a session ran, taken from the path, not a
// *decision* about what it was for. That distinction is why it needs no
// manifest: labelling by path claims nothing, whereas inferring a boundary
// from a path would be the path-guessing docs/design.md forbids by name.
//
// Returns "" when path is not under root, which is not an error: a run with no
// attributable lane is still a real run with a real cost, and is reported as
// unattributed rather than dropped or guessed at.
func LaneFromPath(root, path string) string {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return ""
	}
	first, rest, ok := strings.Cut(rel, "/")
	if !ok || rest == "" {
		// A file sitting directly in root belongs to no project directory.
		return ""
	}
	return first
}

// LaneDisplay shortens a lane for display. Lanes are slugged absolute paths
// ("-Users-bertie-projects-personal-dotfiles"), which are faithful but wide.
// The slug is never un-slugged: the slugging replaces every separator with a
// hyphen, so a directory name containing a hyphen is indistinguishable from a
// separator and reversing it would invent paths that do not exist.
func LaneDisplay(lane string) string {
	if lane == "" {
		return "(unattributed)"
	}
	parts := strings.Split(strings.TrimPrefix(lane, "-"), "-")
	if len(parts) <= 2 {
		return strings.Join(parts, "/")
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}
