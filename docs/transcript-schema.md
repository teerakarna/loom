# Transcript schema notes

This is the contract everything else in Loom depends on (see `docs/design.md`, "The enabling
fact"). Get this wrong and no downstream cost figure means anything.

**Status: first pass, grounded in direct inspection of real transcript files on one machine
(one user, one Claude Code version) — not yet a synthetic-fixture-verified schema.** Treat
anything below as "observed to exist" rather than "guaranteed stable across versions." Before
any ingest code ships, this needs the same field list re-derived from Claude Code's own source
or documentation if that becomes available, and pinned against synthetic fixtures per the
design doc's publishability rules (no fixture may be derived from a real transcript, even
redacted).

## Location

One JSONL file per session: `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl`.
Subagent runs get their own directory: `~/.claude/projects/<slugified-cwd>/<session-id>/subagents/`,
one `agent-<agentId>.jsonl` plus a companion `agent-<agentId>.meta.json` per agent. The `.meta.json`
companion was **not** anticipated in the original design doc — it exists and is worth using: at
minimum it carries `agentType`, `description`, `toolUseId`, `spawnDepth`.

## Top-level line types

Observed `type` values in one real transcript, beyond the two the design doc named
(`user`, `assistant`): `system`, `attachment`, `pr-link`, `atis-latch`, `last-prompt`,
`file-history-delta`, `file-history-snapshot`, `queue-operation`, `agent-name`, `ai-title`,
`mode`, `permission-mode`. Loom's ingest should treat unknown types as inert rather than erroring
— new ones will appear across Claude Code versions, and per the design doc's constraint 3
(schema-tolerant), an unrecognised line must never block ingest of the rest of the file.

Every line observed so far carries: `uuid`, `parentUuid`, `isSidechain`, `timestamp`, `sessionId`
and/or `session_id` (both forms seen — do not assume only one is present), `userType`,
`entrypoint`, `cwd`, `version` (Claude Code version string), `gitBranch`. `sessionKind` was seen
with value `"bg"` on a background-job session — worth capturing since cost/behavior patterns
plausibly differ for background vs interactive sessions.

## `assistant` lines

`message.content` is an array of blocks; observed block types: `thinking`, `text`, `tool_use`.

`message.usage` — confirmed real shape, richer than the design doc's flat list:

```json
{
  "input_tokens": 0,
  "cache_creation_input_tokens": 0,
  "cache_read_input_tokens": 0,
  "output_tokens": 0,
  "server_tool_use": { "web_search_requests": 0, "web_fetch_requests": 0 },
  "service_tier": "standard",
  "cache_creation": { "ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 0 },
  "inference_geo": "not_available",
  "iterations": [ { "input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0,
                     "cache_creation_input_tokens": 0, "cache_creation": {"...": "..."},
                     "type": "message" } ],
  "speed": "standard"
}
```

Notes for the cost model (design doc: "weighted cost per run... get this right first"):

- `cache_creation` splits by TTL (`ephemeral_1h_input_tokens` vs `ephemeral_5m_input_tokens`) —
  these plausibly have different effective costs and must not be summed as one undifferentiated
  "cache write" figure.
- `iterations` is an array — a single assistant line can represent more than one internal
  model round-trip (e.g. a tool-forced retry), each with its own token breakdown. Summing top-level
  `usage` alone vs summing `iterations` may or may not agree; this needs checking against a real
  multi-iteration example before the ingest cost function is trusted.
- `service_tier` and `speed` are both present and both string enums (`"standard"` observed) —
  unclear yet whether these ever diverge or one is derived from the other.
- `message.model` is a real field (e.g. `"claude-opus-4-8"`) as the design doc expected.
- `effort` (reasoning effort) was asserted by the design doc but not yet independently confirmed
  present on an observed line — may be model-conditional. Needs a targeted check against a
  high-effort-model transcript before ingest code assumes it's always there.

Other assistant-line fields observed: `requestId`, `attributionMcpServer` / `attributionMcpTool`
(populated when the turn's context came from an MCP tool result — e.g. `"claude.ai Google Drive"`
/ `"search_files"`), `stop_reason`, `stop_sequence`, `stop_details`, `diagnostics`.

`durationMs` (camelCase — **not** `duration_ms`) is a real, confirmed field. The design doc used
snake_case; ingest code must not assume snake_case for this one.

## `user` lines

`message.content` is either a plain string or (for tool results returning to the model) a
richer structure — not yet fully characterized. `isCompactSummary` and
`isVisibleInTranscriptOnly` are real boolean fields seen on a context-compaction summary line;
Loom should probably exclude or specially tag compaction-summary content rather than counting it
as ordinary user input.

## Tool denial and user feedback

Both design-doc-asserted fields are confirmed real and populated:

- `toolDenialKind` — observed values: `"user-rejected"`, `"automode-blocked"`. This is a rework
  signal per the design doc's "Measured" list.
- `userFeedback` — free-text string, e.g. a user's correction attached to a turn. Also a rework
  signal, and likely needs its own light classification later (correction vs. clarification vs.
  praise) rather than being treated as one undifferentiated bucket — out of scope for B1.

## Subagent completion — not yet located

The design doc asserts the parent transcript records `subagent_tokens`, `tool_uses`, and
`duration_ms` in a task-completion notification, giving an independent figure to reconcile
computed subagent costs against. A grep for `subagent_tokens` and `tool_uses` across one real
transcript containing several subagent launches found the **launch** record
(`toolUseResult` with `isAsync`, `status: "async_launched"`, `agentId`, `description`,
`resolvedModel`, `prompt`) but not yet a completion record with those exact field names.

**This is a real open question, not a confirmed fact — flag it clearly in code comments and do
not hardcode ingest logic against `subagent_tokens`/`tool_uses` until a completion record has
actually been found and inspected.** The design doc's own "Ingest correctness" verification step
(replay fixtures, assert computed subagent totals reconcile against the transcript's own
recorded totals) is exactly the check that will surface whether this field exists, is named
differently, or has to be computed by summing the subagent's own `agent-<id>.jsonl` instead.

## Open items before B1 ingest code is trusted

1. Locate and confirm the actual subagent completion-notification shape (see above).
2. Confirm whether `effort` is present, and under what conditions.
3. Confirm whether summing `iterations[].{input,output,cache_*}_tokens` reconciles with the
   top-level `usage` object, or whether one is a subset/rollup of the other.
4. Build the synthetic fixtures (hand-written, never derived from a real transcript per the
   design doc's publishability rules) that exercise every line type and field documented here.
