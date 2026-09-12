package artifact

import (
	"bufio"
	"os"
	"strings"
)

// frontmatter is the subset of a Markdown file's YAML frontmatter Loom
// actually uses. Design doc constraint 3 (schema-tolerant): this is a
// deliberately shallow, line-based reader, not a YAML parser — it reads
// whatever "key: value" pairs exist at the top level and ignores everything
// else (nested maps, lists, multi-line scalars). Missing fields come back as
// empty strings, never an error, because different artifact conventions in
// the wild use different frontmatter shapes and Loom must not assume one.
type frontmatter struct {
	Name        string
	Description string
}

// readFrontmatter reads the "---" delimited YAML frontmatter block at the top
// of path, if any. Returns a zero frontmatter (not an error) when the file
// has no frontmatter, is unreadable, or the block never closes — discovery
// must never fail on a file it doesn't understand.
func readFrontmatter(path string) frontmatter {
	f, err := os.Open(path)
	if err != nil {
		return frontmatter{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return frontmatter{}
	}

	var fm frontmatter
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			return fm
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			fm.Name = value
		case "description":
			fm.Description = value
		}
	}
	return frontmatter{} // block never closed — treat as no frontmatter
}

// headingScanLimit bounds how far into a file firstHeading reads before
// giving up — real headings sit near the top; a file with none within this
// many lines is treated as having no fallback description rather than
// scanning arbitrarily large files for one.
const headingScanLimit = 50

// firstHeading returns the text of the first level-1 Markdown heading
// ("# Title") in path, or "" if none appears within headingScanLimit lines.
// Used as a description fallback for artifacts with no frontmatter at all —
// a real case (see internal/artifact tests, and skills observed in the
// wild), not a hypothetical one: design doc constraint 3 says frontmatter is
// schema-tolerant, and "no frontmatter, just a heading" is one more schema
// to tolerate.
func firstHeading(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for i := 0; i < headingScanLimit && sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if after, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
