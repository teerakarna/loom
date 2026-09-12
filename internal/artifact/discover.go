// Package artifact discovers the artifacts a user already has — skills,
// agents, plans, hooks, and per-project memory — by scanning the standard
// Claude Code locations and the current project. See docs/design.md, design
// constraint 2 ("Discover, never assume") and constraint 3
// ("Schema-tolerant"): there is no required layout or naming convention, and
// a location that doesn't exist, or a file this package can't parse, is
// silently skipped rather than treated as an error.
package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Kinds of artifact this package can discover. Scratch and Workflow (see
// docs/design.md, "The model") aren't included: scratch is explicitly a
// read-only inbox with no fixed location to enumerate, and workflows don't
// exist as a distinct Claude Code artifact type yet.
const (
	KindSkill  = "skill"
	KindAgent  = "agent"
	KindPlan   = "plan"
	KindHook   = "hook"
	KindMemory = "memory"
)

// Artifact is one discovered artifact instance. Description is best-effort —
// empty when the source file has no frontmatter, or no frontmatter this
// package recognizes (see frontmatter.go).
type Artifact struct {
	Kind        string
	Path        string
	Name        string
	Description string
}

// Locations is the set of directories and files Discover scans, resolved
// once so callers (and tests) can see and override exactly where discovery
// looks — Discover itself takes no arguments.
type Locations struct {
	SkillDirs     []string
	AgentDirs     []string
	PlanDirs      []string
	SettingsFiles []string
	MemoryDirs    []string
}

// DefaultLocations resolves the standard Claude Code locations under home,
// plus the current project's own .claude/ directory and its per-project
// memory directory under home. cwd is the project root to treat as "the
// current project" — callers pass the working directory, not necessarily a
// git root, matching how Claude Code itself keys project state.
func DefaultLocations(home, cwd string) Locations {
	return Locations{
		SkillDirs: []string{
			filepath.Join(home, ".claude", "skills"),
			filepath.Join(cwd, ".claude", "skills"),
		},
		AgentDirs: []string{
			filepath.Join(home, ".claude", "agents"),
			filepath.Join(cwd, ".claude", "agents"),
		},
		PlanDirs: []string{
			filepath.Join(home, ".claude", "plans"),
			filepath.Join(cwd, ".claude", "plans"),
		},
		SettingsFiles: []string{
			filepath.Join(home, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.json"),
		},
		MemoryDirs: []string{
			filepath.Join(home, ".claude", "projects", projectSlug(cwd), "memory"),
		},
	}
}

// projectSlug reproduces the slug Claude Code derives from a project's
// working directory to key its per-project state under ~/.claude/projects/
// (observed empirically: every path separator becomes a hyphen).
func projectSlug(cwd string) string {
	return strings.ReplaceAll(cwd, string(filepath.Separator), "-")
}

// Discover scans every location in locs and returns every artifact found.
// Order is not significant. A location that doesn't exist is skipped, not an
// error — most users will have some but not all of these (design doc
// constraint 1, useful at n=0; constraint 2, never assume a type is in use).
func Discover(locs Locations) ([]Artifact, error) {
	var out []Artifact

	for _, d := range locs.SkillDirs {
		found, err := scanMarkdownDir(d, KindSkill)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	for _, d := range locs.AgentDirs {
		found, err := scanMarkdownDir(d, KindAgent)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	for _, d := range locs.PlanDirs {
		found, err := scanMarkdownDir(d, KindPlan)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	for _, d := range locs.MemoryDirs {
		found, err := scanMarkdownDir(d, KindMemory)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	for _, f := range locs.SettingsFiles {
		found := scanHooks(f)
		out = append(out, found...)
	}

	return out, nil
}

// scanMarkdownDir finds artifacts of kind in dir. Two conventions are
// recognized side by side, because both exist in the wild for skills and
// nothing here should force a choice between them (design doc constraint 3):
// a plain "name.md" file directly in dir, or a subdirectory containing its
// own "SKILL.md". A missing dir is not an error.
func scanMarkdownDir(dir, kind string) ([]Artifact, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Artifact
	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(dir, e.Name())
			path := filepath.Join(sub, "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				continue // a subdirectory with no SKILL.md isn't an artifact this package recognizes
			}
			out = append(out, artifactFromFile(path, e.Name(), kind))
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		name := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, artifactFromFile(path, name, kind))
	}
	return out, nil
}

// maxDescriptionRunes bounds every artifact description Loom stores and
// later re-serves through get_recommendation (design doc constraint 9,
// "artifact-derived text is data, never instructions"). This is read off
// disk, unsanitized, from files Loom does not control the contents of — a
// length cap doesn't stop an adversarial description from being adversarial,
// but it stops one from being unboundedly large in whatever context a
// downstream client renders it into.
const maxDescriptionRunes = 300

// artifactFromFile builds an Artifact from a Markdown file, preferring the
// frontmatter's own name over the filename-derived fallback when present. A
// file with no frontmatter description at all (plain Markdown, no YAML
// block — a real convention, not hypothetical) falls back to its first `#`
// heading, so the selector still has something to score it against.
func artifactFromFile(path, fallbackName, kind string) Artifact {
	fm := readFrontmatter(path)
	name := fallbackName
	if fm.Name != "" {
		name = fm.Name
	}
	desc := fm.Description
	if desc == "" {
		desc = firstHeading(path)
	}
	return Artifact{Kind: kind, Path: path, Name: name, Description: truncate(desc, maxDescriptionRunes)}
}

// truncate returns s unchanged if it's within max runes, or its first
// max-1 runes plus an ellipsis otherwise. Operates on runes, not bytes, so a
// multi-byte character is never split.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// hooksSettings is the subset of settings.json this package reads. Every
// other key is ignored — schema-tolerant per design doc constraint 3, and a
// file that isn't valid JSON, or doesn't exist, produces no artifacts rather
// than an error (constraint 7, degrade never block).
type hooksSettings struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// scanHooks reads the hook entries out of a settings.json file. Each
// configured command becomes one artifact; its Path is synthetic (settings
// files hold many hooks, not one per file) but stable across runs, which is
// what upsert-by-path bookkeeping needs.
func scanHooks(path string) []Artifact {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s hooksSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}

	var out []Artifact
	for event, matchers := range s.Hooks {
		for mi, m := range matchers {
			for hi, h := range m.Hooks {
				if h.Type != "command" || h.Command == "" {
					continue
				}
				out = append(out, Artifact{
					Kind: KindHook,
					Path: hookPath(path, event, mi, hi),
					Name: event,
				})
			}
		}
	}
	return out
}

func hookPath(settingsPath, event string, matcherIdx, hookIdx int) string {
	return settingsPath + "::hooks:" + event + ":" + strconv.Itoa(matcherIdx) + ":" + strconv.Itoa(hookIdx)
}
