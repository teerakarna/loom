package mcp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The ingest hook runs after every turn in every session, so what it must
// never do is get in the way: fail, print, or hang. These run the real wrapper
// script against a fake `loom`, since a config that parses says nothing about
// whether the script behaves.

func TestPluginHookConfig(t *testing.T) {
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	readJSON(t, "plugin/hooks/hooks.json", &cfg)

	if len(cfg.Hooks) != 1 {
		t.Errorf("hooks.json registers events %v, want only Stop: nothing else was agreed", keys(cfg.Hooks))
	}
	groups := cfg.Hooks["Stop"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("want exactly one Stop command hook, got %+v", cfg.Hooks)
	}
	h := groups[0].Hooks[0]
	if h.Type != "command" {
		t.Errorf("hook type is %q, want command", h.Type)
	}
	if !h.Async {
		t.Error("hook is not async: it would delay the end of every turn, and Claude Code enforces no timeout on async hooks only because the script has its own")
	}
	const root = "${CLAUDE_PLUGIN_ROOT}"
	if !strings.Contains(h.Command, root) {
		t.Fatalf("command %q is not rooted at %s", h.Command, root)
	}
	rel := strings.TrimPrefix(strings.Trim(strings.TrimPrefix(h.Command, "\""+root+"\""), "\" "), "/")
	info, err := os.Stat(filepath.Join(repoRoot, "plugin", rel))
	if err != nil {
		t.Fatalf("command points at %q, which does not exist: %v", rel, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable (mode %v)", rel, info.Mode())
	}
}

func runHook(t *testing.T, env []string) (string, int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(repoRoot, "plugin", "bin", "loom-ingest"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// fakeLoom writes a stand-in binary that records its arguments, and returns
// its path and the file it records to.
func fakeLoom(t *testing.T, body string) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "called")
	bin = filepath.Join(dir, "loom")
	script := "#!/bin/sh\necho \"$@\" >> " + record + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func TestHookRunsIngestAndStaysSilent(t *testing.T) {
	bin, record := fakeLoom(t, "echo noise; echo more-noise >&2")
	out, code := runHook(t, []string{"LOOM_BIN=" + bin})
	if code != 0 || out != "" {
		t.Errorf("exit %d, output %q, want exit 0 and no output even when loom prints", code, out)
	}
	got, _ := os.ReadFile(record)
	if strings.TrimSpace(string(got)) != "ingest" {
		t.Errorf("loom was called with %q, want exactly: ingest", got)
	}
}

func TestHookExitsZeroWhenIngestFails(t *testing.T) {
	bin, _ := fakeLoom(t, "exit 7")
	if out, code := runHook(t, []string{"LOOM_BIN=" + bin}); code != 0 || out != "" {
		t.Errorf("exit %d, output %q: a failing ingest must not fail the hook", code, out)
	}
}

// The counterpoints: each way of not running must really not run it.
func TestHookDoesNothingWhenOptedOutOrBinaryMissing(t *testing.T) {
	bin, record := fakeLoom(t, "")
	if out, code := runHook(t, []string{"LOOM_NO_HOOK=1", "LOOM_BIN=" + bin}); code != 0 || out != "" {
		t.Errorf("opted out: exit %d, output %q", code, out)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("LOOM_NO_HOOK=1 still ran loom")
	}
	if out, code := runHook(t, []string{"LOOM_BIN=/does/not/exist"}); code != 0 || out != "" {
		t.Errorf("bad LOOM_BIN: exit %d, output %q, want a silent exit 0", code, out)
	}
	// The script also checks two absolute paths that the test cannot redirect;
	// on a machine with a real install there, this case would pass vacuously.
	for _, p := range []string{"/usr/local/bin/loom", "/opt/homebrew/bin/loom"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists, so the no-binary case cannot be tested here", p)
		}
	}
	if out, code := runHook(t, nil); code != 0 || out != "" {
		t.Errorf("no binary anywhere: exit %d, output %q, want a silent exit 0", code, out)
	}
}

// loom-ingest repeats loom-mcp's lookup on purpose (see its header). This is
// what stops the two drifting: an install location added to one and not the
// other would make the server start and the hook silently do nothing.
func TestHookAndServerLookInTheSamePlaces(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(repoRoot, "plugin", "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mcp, hook := read("loom-mcp"), read("loom-ingest")
	for _, loc := range []string{`"${GOBIN:-}/loom"`, `"${GOPATH:-$HOME/go}/bin/loom"`, `"$HOME/go/bin/loom"`,
		"/opt/homebrew/bin/loom", "/usr/local/bin/loom"} {
		if !strings.Contains(mcp, loc) {
			t.Errorf("loom-mcp no longer lists %s; update this test and loom-ingest together", loc)
		}
		if !strings.Contains(hook, loc) {
			t.Errorf("loom-ingest does not list %s, which loom-mcp does", loc)
		}
	}
}

// 0 and false are the values a user reaches for to mean "enabled".
func TestHookOptOutOnlyForRealValues(t *testing.T) {
	for _, v := range []string{"0", "false", ""} {
		bin, record := fakeLoom(t, "")
		runHook(t, []string{"LOOM_NO_HOOK=" + v, "LOOM_BIN=" + bin})
		if _, err := os.Stat(record); err != nil {
			t.Errorf("LOOM_NO_HOOK=%q turned the hook off", v)
		}
	}
}

// The cap is the only thing stopping a wedged ingest from piling up one process
// per turn, since Claude Code enforces no timeout on async hooks.
func TestHookKillsAHungIngest(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin, _ := fakeLoom(t, "echo $$ > "+pidFile+"\nexec sleep 60")
	start := time.Now()
	out, code := runHook(t, []string{"LOOM_BIN=" + bin, "LOOM_HOOK_TIMEOUT=2"})
	if code != 0 || out != "" {
		t.Errorf("exit %d, output %q, want a silent exit 0", code, out)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("hook took %s with a 2s cap, so the hung ingest was not killed", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-0", strings.TrimSpace(string(b))).Run(); err == nil {
		t.Error("the hung ingest process is still running")
	}
}
