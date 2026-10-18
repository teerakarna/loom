package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Claude Code plugin's entire content is a registration of this MCP
// server, which is why its checks live in this package rather than off on
// their own: the coupling being tested is to `version` below, and to the fact
// that `loom serve` is what the wrapper execs.
//
// None of this is exercised by building or running loom. A typo'd path or a
// wrapper that lost its executable bit would ship green, and would surface as
// an MCP server that silently fails to start in someone else's session. These
// are cheap assertions against exactly that.

const repoRoot = "../.."

func readJSON(t *testing.T, rel string, into any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s is not valid JSON: %v", rel, err)
	}
}

func TestPluginManifest(t *testing.T) {
	var m struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		MCPServers string `json:"mcpServers"`
		License    string `json:"license"`
	}
	readJSON(t, "plugin/.claude-plugin/plugin.json", &m)

	if m.Name != "loom" {
		t.Errorf("plugin name is %q, want %q - it is the install address (loom@loom)", m.Name, "loom")
	}

	// The manifest version is the MCP contract's version, not the binary's.
	// They are allowed to differ from each other in what they mean but not in
	// what they say, or `/plugin` reports a version that promises tool shapes
	// the server does not have.
	if want := strings.TrimPrefix(version, "v"); m.Version != want {
		t.Errorf("plugin.json version is %q but internal/mcp version is %q. Bump both, or the "+
			"plugin advertises a tool contract the server is not serving", m.Version, version)
	}

	if m.License != "Apache-2.0" {
		t.Errorf("plugin.json license is %q, want Apache-2.0 to match LICENSE", m.License)
	}

	if m.MCPServers == "" {
		t.Fatal("plugin.json declares no mcpServers - the plugin would install and do nothing")
	}
	cfg := filepath.Join(repoRoot, "plugin", filepath.Clean(m.MCPServers))
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("plugin.json points mcpServers at %q, which does not exist: %v", m.MCPServers, err)
	}
}

func TestPluginMCPConfig(t *testing.T) {
	var servers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	readJSON(t, "plugin/.mcp.json", &servers)

	srv, ok := servers["loom"]
	if !ok {
		t.Fatalf("plugin/.mcp.json has no \"loom\" server; got keys %v", keys(servers))
	}

	const root = "${CLAUDE_PLUGIN_ROOT}/"
	if !strings.HasPrefix(srv.Command, root) {
		t.Fatalf("command is %q. It must be rooted at %s - a bare name or a relative path resolves "+
			"against whatever directory the client happened to start in", srv.Command, root)
	}

	// The wrapper has to exist and has to be executable. A cloned plugin gets
	// the mode bits git recorded, so losing +x here means every install is
	// broken and nothing in a normal build would notice.
	rel := strings.TrimPrefix(srv.Command, root)
	info, err := os.Stat(filepath.Join(repoRoot, "plugin", filepath.Clean(rel)))
	if err != nil {
		t.Fatalf("command points at %q, which does not exist: %v", rel, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable (mode %v). Claude Code would fail to launch the server",
			rel, info.Mode())
	}
}

func TestMarketplaceEntry(t *testing.T) {
	var mk struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	readJSON(t, ".claude-plugin/marketplace.json", &mk)

	if mk.Name == "" || mk.Owner.Name == "" {
		t.Error("marketplace.json needs both name and owner.name; both are required fields")
	}
	if len(mk.Plugins) != 1 {
		t.Fatalf("expected exactly 1 plugin entry, got %d", len(mk.Plugins))
	}

	entry := mk.Plugins[0]
	manifest := filepath.Join(repoRoot, filepath.Clean(entry.Source), ".claude-plugin", "plugin.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("marketplace entry sources %q, which has no .claude-plugin/plugin.json: %v",
			entry.Source, err)
	}

	var m struct {
		Name string `json:"name"`
	}
	readJSON(t, filepath.Join(filepath.Clean(entry.Source), ".claude-plugin", "plugin.json"), &m)
	if entry.Name != m.Name {
		t.Errorf("marketplace calls the plugin %q, its own manifest calls it %q. The install "+
			"command uses the marketplace name and the manifest name has to agree", entry.Name, m.Name)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// releasePleaseProblems says what would stop release-please bumping every
// version string: the annotation on the const line and an extra-files entry
// whose path is exactly each file. Pure, so a test can show it catches a miss.
func releasePleaseProblems(serverSrc string, extraFiles []json.RawMessage) []string {
	var problems []string
	found, annotated := false, false
	for _, line := range strings.Split(serverSrc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "const version =") {
			found = true
			annotated = strings.Contains(line, "x-release-please-version")
		}
	}
	switch {
	case !found:
		problems = append(problems, `no "const version =" line in server.go; if it moved into a const block, release-please needs its annotation on the new line and this check needs updating`)
	case !annotated:
		problems = append(problems, `the "const version =" line has no x-release-please-version comment`)
	}

	listed := map[string]bool{}
	for _, raw := range extraFiles {
		var plain string
		var obj struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &plain) == nil {
			listed[plain] = true
		} else if json.Unmarshal(raw, &obj) == nil {
			listed[obj.Path] = true
		}
	}
	for _, want := range []string{"internal/mcp/server.go", "plugin/.claude-plugin/plugin.json"} {
		if !listed[want] {
			problems = append(problems, "extra-files does not list "+want)
		}
	}
	return problems
}

func TestReleasePleaseCoversEveryVersionString(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Packages map[string]struct {
			ExtraFiles []json.RawMessage `json:"extra-files"`
		} `json:"packages"`
	}
	readJSON(t, "release-please-config.json", &cfg)
	for _, p := range releasePleaseProblems(string(src), cfg.Packages["."].ExtraFiles) {
		t.Error(p)
	}

	var manifest map[string]string
	readJSON(t, ".release-please-manifest.json", &manifest)
	var plugin struct {
		Version string `json:"version"`
	}
	readJSON(t, "plugin/.claude-plugin/plugin.json", &plugin)
	if manifest["."] != plugin.Version {
		t.Errorf(".release-please-manifest.json says %q but plugin.json says %q; they move together or release-please bumps from the wrong base",
			manifest["."], plugin.Version)
	}
}

// The check above is only worth having if it can fail.
func TestReleasePleaseProblemsDetectsAMiss(t *testing.T) {
	good := []json.RawMessage{
		json.RawMessage(`{"type":"json","path":"plugin/.claude-plugin/plugin.json","jsonpath":"$.version"}`),
		json.RawMessage(`"internal/mcp/server.go"`),
	}
	if p := releasePleaseProblems("const version = \"v1.2.3\" // x-release-please-version\n", good); len(p) != 0 {
		t.Errorf("a correct setup reported problems: %v", p)
	}
	if p := releasePleaseProblems("const version = \"v1.2.3\"\n", good); len(p) != 1 {
		t.Errorf("a missing annotation gave %v, want one problem", p)
	}
	if p := releasePleaseProblems("package mcp\n", good); len(p) != 1 {
		t.Errorf("a missing const line gave %v, want one problem", p)
	}
	decoy := []json.RawMessage{
		json.RawMessage(`{"type":"json","path":"other.json","jsonpath":"$.x","note":"internal/mcp/server.go plugin/.claude-plugin/plugin.json"}`),
	}
	if p := releasePleaseProblems("const version = \"v1\" // x-release-please-version\n", decoy); len(p) != 2 {
		t.Errorf("paths mentioned only inside another field must not count, got %v", p)
	}
}
