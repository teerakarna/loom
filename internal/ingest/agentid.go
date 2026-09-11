package ingest

import (
	"path/filepath"
	"strings"
)

// AgentIDFromPath extracts the agent id from a subagent transcript path
// (".../subagents/agent-<id>.jsonl" -> "<id>"), matching the id used both in
// the filename and as the <task-id> in that agent's completion notification
// in the parent transcript. Returns ok=false for a non-agent path.
func AgentIDFromPath(path string) (string, bool) {
	base := filepath.Base(path)
	const prefix, suffix = "agent-", ".jsonl"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, suffix) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(base, prefix), suffix), true
}
