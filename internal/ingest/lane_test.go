package ingest

import "testing"

func TestLaneFromPath(t *testing.T) {
	const root = "/home/u/.claude/projects"
	cases := []struct {
		name, path, want string
	}{
		{"session transcript", root + "/-home-u-projects-dotfiles/abc.jsonl", "-home-u-projects-dotfiles"},
		// A subagent belongs to the lane its parent session ran in, which is
		// the whole reason overlap is not a problem.
		{"subagent transcript", root + "/-home-u-projects-dotfiles/abc/subagents/agent-x.jsonl", "-home-u-projects-dotfiles"},
		{"different lane", root + "/-home-u-work-azpocket/def.jsonl", "-home-u-work-azpocket"},
		{"file directly in root", root + "/stray.jsonl", ""},
		{"outside root", "/somewhere/else/abc.jsonl", ""},
		{"root itself", root, ""},
		{"trailing slash on root", root + "/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LaneFromPath(root, c.path); got != c.want {
				t.Errorf("LaneFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestLaneDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "(unattributed)"},
		{"-home-u-projects-personal-dotfiles", ".../personal/dotfiles"},
		{"-home-u", "home/u"},
		{"-a-b-c", ".../b/c"},
	}
	for _, c := range cases {
		if got := LaneDisplay(c.in); got != c.want {
			t.Errorf("LaneDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A lane slug is never un-slugged. Slugging replaces every separator with a
// hyphen, so a directory whose name contains a hyphen is indistinguishable
// from a separator, and reversing it would invent paths that never existed.
func TestLaneDisplayDoesNotInventPaths(t *testing.T) {
	got := LaneDisplay("-home-u-my-repo")
	if got != ".../my/repo" {
		t.Errorf("LaneDisplay = %q", got)
	}
	// The point: display is lossy and for humans. The stored value is intact.
	if LaneFromPath("/root", "/root/-home-u-my-repo/a.jsonl") != "-home-u-my-repo" {
		t.Error("the stored lane must remain the exact slug")
	}
}
