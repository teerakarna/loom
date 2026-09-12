package ingest

import (
	"encoding/json"
	"os"
	"strings"
)

// AgentMeta is the ".meta.json" companion written alongside a subagent's
// transcript. Confirmed shape, from real files:
//
//	{"agentType":"Explore","description":"...","toolUseId":"toolu_...","spawnDepth":1}
//
// This is the only place a run's agent type is recorded - the transcript
// itself never names it - so B3's per-agent-type policy has nothing to key on
// without reading these. Description is deliberately NOT stored in the ledger:
// it is free text a user wrote, and constraint 6 (privacy by construction)
// keeps content out of the database.
// The file also carries "description" and "toolUseId". Neither is declared
// here, deliberately: description is free text a user wrote, and unmarshalling
// only the fields Loom actually stores is the cheapest way to guarantee
// content never reaches the ledger by accident.
type AgentMeta struct {
	AgentType  string `json:"agentType"`
	SpawnDepth int    `json:"spawnDepth"`
}

// ReadAgentMeta reads the ".meta.json" companion for a subagent transcript at
// path (".../agent-<id>.jsonl" -> ".../agent-<id>.meta.json"). Returns ok=false
// when there is no companion, it cannot be read, or it is not valid JSON:
// every one of those is a normal condition on a corpus written by a different
// tool, not an error worth failing an ingest over (design constraint 3,
// schema-tolerant).
func ReadAgentMeta(path string) (AgentMeta, bool) {
	if !strings.HasSuffix(path, ".jsonl") {
		return AgentMeta{}, false
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
	if err != nil {
		return AgentMeta{}, false
	}
	var m AgentMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return AgentMeta{}, false
	}
	if m.AgentType == "" {
		return AgentMeta{}, false
	}
	return m, true
}
