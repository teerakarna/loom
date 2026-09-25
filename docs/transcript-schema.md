# Transcript schema notes

This is the contract everything else in Loom depends on (see `docs/design.md`, "The enabling
fact"). Get this wrong and no downstream cost figure means anything.

**Status: first pass, grounded in direct inspection of real transcript files on one machine
(one user, one Claude Code version) - not yet a synthetic-fixture-verified schema.** Treat
anything below as "observed to exist" rather than "guaranteed stable across versions." Before
any ingest code ships, this needs the same field list re-derived from Claude Code's own source
or documentation if that becomes available, and pinned against synthetic fixtures per the
design doc's publishability rules (no fixture may be derived from a real transcript, even
redacted).

## Location

One JSONL file per session: `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl`.
Subagent runs get their own directory: `~/.claude/projects/<slugified-cwd>/<session-id>/subagents/`,
one `agent-<agentId>.jsonl` plus a companion `agent-<agentId>.meta.json` per agent. The `.meta.json`
companion was **not** anticipated in the original design doc - it exists and is worth using: at
minimum it carries `agentType`, `description`, `toolUseId`, `spawnDepth`.

**`agentType` is only available here.** The transcript itself never names the agent type, so
without reading the companion there is nothing to attribute a run to, and per-agent-type policy
(B3) has no key. Confirmed 2026-09-12 on a real corpus: 18 of 18 agent transcripts had a readable
companion, `agentType` values `Explore` (14) and `fork` (4), all at `spawnDepth: 1`. Loom reads
`agentType` and `spawnDepth` only, deliberately ignoring `description` (free text a user wrote,
kept out of the ledger by constraint 6).

A missing or unparseable companion leaves the agent type empty rather than failing the ingest:
the run's cost is still real and worth recording, it just cannot be attributed.

**The subagents directory is not flat, and not everything in it is a transcript.** A workflow run
nests its own agents one level deeper, at `subagents/workflows/<wf-id>/agent-<id>.jsonl`, alongside
a `subagents/workflows/<wf-id>/journal.jsonl` that is **not** a transcript: it holds orchestration
records only (`started` / `result`, keyed by `agentId`), no assistant turns and no `usage`.

Measured on a real corpus 2026-09-25: 169 flat `agent-<id>.jsonl`, 42 nested under
`workflows/<wf-id>/`, 1 `journal.jsonl`, and nothing under `subagents/` lacking the `agent-` prefix
that was a real transcript. So the discriminator is the **filename prefix**, not the nesting depth -
a depth rule would have silently dropped all 42 workflow subagent runs. `ingest.IsTranscript` is
where that lives, and ingesting the journal by mistake produced a run with no model, no tokens and
no tool calls that then sat in the by-model breakdown and the policy denominators.

## Top-level line types

Observed `type` values in one real transcript, beyond the two the design doc named
(`user`, `assistant`): `system`, `attachment`, `pr-link`, `atis-latch`, `last-prompt`,
`file-history-delta`, `file-history-snapshot`, `queue-operation`, `agent-name`, `ai-title`,
`mode`, `permission-mode`. Loom's ingest should treat unknown types as inert rather than erroring
- new ones will appear across Claude Code versions, and per the design doc's constraint 3
(schema-tolerant), an unrecognised line must never block ingest of the rest of the file.

Every line observed so far carries: `uuid`, `parentUuid`, `isSidechain`, `timestamp`, `sessionId`
and/or `session_id` (both forms seen - do not assume only one is present), `userType`,
`entrypoint`, `cwd`, `version` (Claude Code version string), `gitBranch`. `sessionKind` was seen
with value `"bg"` on a background-job session - worth capturing since cost/behavior patterns
plausibly differ for background vs interactive sessions.

## `assistant` lines

`message.content` is an array of blocks; observed block types: `thinking`, `text`, `tool_use`.

### One response spans several lines, each repeating the same usage

**This is the single most expensive thing to get wrong in this file.** One API response is written
as **multiple JSONL lines, one per content block**, and every one of those lines carries the
**same** `message.usage` object and the same `message.id`:

```
message id: msg_01JNrMpGgM5TYKmxKywz appears 4 times
   cache_read=26284 output=290 content_blocks=['thinking']
   cache_read=26284 output=290 content_blocks=['text']
   cache_read=26284 output=290 content_blocks=['tool_use']
   cache_read=26284 output=290 content_blocks=['tool_use']
   -> identical usage on every line: True
```

So **usage must be counted once per distinct `message.id`, not once per line**. Summing per line
overstated weighted cost by **2.12x** across this machine's 21 real transcripts (one file had 2952
assistant lines for 1339 unique ids, worst id repeated 11 times, 57.7% of its cache-read total
being re-counted duplicates).

Worse than the absolute error: the inflation scales with how many content blocks a response had, so
responses with many tool calls are over-weighted relative to plain text ones. Model and agent-type
comparisons are skewed, not just the total.

`tool_use` counting is the exact opposite and must **not** be deduplicated: each line carries its
own distinct content block, so two `tool_use` blocks on two lines of the same response are two
genuinely separate tool calls.

Found by running `loom report` against real history rather than by reading fixtures, which is
the third time this format has not been what it looked like. See
`internal/ingest.applyEvent` and the regression tests in `run_test.go`.

`message.usage` - confirmed real shape, richer than the design doc's flat list:

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

- `cache_creation` splits by TTL (`ephemeral_1h_input_tokens` vs `ephemeral_5m_input_tokens`) -
  these plausibly have different effective costs and must not be summed as one undifferentiated
  "cache write" figure.
- `iterations` is an array - a single assistant line can represent more than one internal
  model round-trip (e.g. a tool-forced retry), each with its own token breakdown. Summing top-level
  `usage` alone vs summing `iterations` may or may not agree; this needs checking against a real
  multi-iteration example before the ingest cost function is trusted.
- `service_tier` and `speed` are both present and both string enums (`"standard"` observed) -
  unclear yet whether these ever diverge or one is derived from the other.
- `message.model` is a real field (e.g. `"claude-opus-4-8"`) as the design doc expected - **with
  one confirmed exception: `"<synthetic>"`.** This is a locally-injected status/error message
  (confirmed example: a rate-limit notice - "You've hit your session limit..."), not a real API
  call, and it always carries all-zero usage. Found by running ingest against real history: a
  failed agent's entire (real, non-zero) cost from earlier in the same file got attributed to
  model `<synthetic>` under a naive "last non-empty model wins" heuristic, because the synthetic
  status message was the last assistant line before the file ended. Ingest must never let this
  value win model attribution for a run.
- `effort` (reasoning effort) is **confirmed present, top level on the line, not inside `message`**
  (checked 2026-09-12: value `"high"` observed). Not universal: 20 of 21 runs on the corpus
  checked carried one, 1 did not, so treat it as optional everywhere. Captured as of B3a and
  stored on `runs.effort`, empty when absent.

Other assistant-line fields observed: `requestId`, `attributionMcpServer` / `attributionMcpTool`
(populated when the turn's context came from an MCP tool result - e.g. `"claude.ai Google Drive"`
/ `"search_files"`), `stop_reason`, `stop_sequence`, `stop_details`, `diagnostics`.

`durationMs` (camelCase - **not** `duration_ms`) is a real, confirmed field. The design doc used
snake_case; ingest code must not assume snake_case for this one.

## `user` lines

`message.content` is either a plain string or (for tool results returning to the model) a
richer structure, now characterized below (B7b). `isCompactSummary` and
`isVisibleInTranscriptOnly` are real boolean fields seen on a context-compaction summary line;
Loom should probably exclude or specially tag compaction-summary content rather than counting it
as ordinary user input.

### `tool_use` / `tool_result` - confirmed shape (B7b)

An assistant line's `tool_use` content block carries the fields ingest needs directly:

```json
{"type": "tool_use", "id": "toolu_011Z1cNbm4oSq6jJ5VszfVHe", "name": "Bash", "input": {"...": "..."}}
```

`id` is what the matching `tool_result` refers back to. The matching result arrives on a later
`user` line, as one block of `message.content`:

```json
{"tool_use_id": "toolu_011Z1cNbm4oSq6jJ5VszfVHe", "type": "tool_result", "content": "...", "is_error": false}
```

`content` is **not always a plain string** - confirmed shapes, by tool: a bare string (the
common case), or a list of blocks: `{"type": "text", "text": "..."}`, `{"type": "image",
"source": {"type": "base64", "data": "..."}}`, `{"type": "document", "source": {...}}`, or
`{"type": "tool_reference", "tool_name": "..."}` (seen on tools whose result is a reference back
to another tool rather than content, e.g. an async agent handoff). Ingest treats `content` as an
opaque byte count - the size of the JSON as written, string or list alike, per docs/design.md's
"bytes are not tokens" rule - and never inspects what is inside it, consistent with constraint 6.

Tool name attribution therefore needs a two-pass join within one file: record `id -> name` from
every `tool_use` block seen, then resolve each `tool_result`'s `tool_use_id` against that map.
`tool_use` always precedes its `tool_result` in file order in every transcript inspected, so a
single forward pass suffices; a `tool_use_id` with no matching block in this file (the call
happened in a transcript this file doesn't contain, e.g. before a resume) is attributed to
`(unknown)` rather than dropped.

### `compact_boundary` - confirmed shape (B7b)

A `{"type": "system", "subtype": "compact_boundary", ...}` line is written whenever the host
compacts context. It is not an `assistant` or `user` line, and carries ground truth directly -
no inference from token deltas needed, and inference was tried and was wrong 42 times out of 42
(docs/design.md, B7b).

```json
{
  "type": "system", "subtype": "compact_boundary", "content": "Conversation compacted",
  "uuid": "0fde28f5-d49d-4383-bb24-2ce204367cb3",
  "compactMetadata": {
    "trigger": "manual",
    "preTokens": 445095,
    "postTokens": 11210,
    "cumulativeDroppedTokens": 1737573,
    "durationMs": 162019,
    "preCompactDiscoveredTools": ["..."],
    "preservedSegment": {"headUuid": "...", "anchorUuid": "...", "tailUuid": "..."},
    "preservedMessages": {"anchorUuid": "...", "uuids": ["..."]}
  }
}
```

`cumulativeDroppedTokens` is cumulative **within one session lineage**, not per event - it is not
safely summable across several compactions in the same corpus. `preTokens - postTokens` is the
per-event drop and is what ingest sums.

**A resumed session replays its prior compaction history verbatim, uuid included.** Confirmed on
this machine: the same `compact_boundary` line, byte-for-byte including its `uuid`, appears in
both the original transcript and the transcript of the session that later resumed it -
`sessionId` differs between the two copies, `uuid` does not. That `uuid` is therefore the
identity ingest dedupes on: summing `cumulativeDroppedTokens` (or even the per-event drop) naively
across every transcript that contains a given `uuid` inflated an early measurement by 1.7M
tokens. Count each `uuid` once, attributed to whichever run's ingest recorded it first.

## Tool denial and user feedback

Both design-doc-asserted fields are confirmed real and populated:

- `toolDenialKind` - observed values: `"user-rejected"`, `"automode-blocked"`. This is a rework
  signal per the design doc's "Measured" list.
- `userFeedback` - free-text string, e.g. a user's correction attached to a turn. Also a rework
  signal, and likely needs its own light classification later (correction vs. clarification vs.
  praise) rather than being treated as one undifferentiated bucket - out of scope for B1.

## Subagent completion - confirmed, but not where the design doc expected

The design doc assumed `subagent_tokens`/`tool_uses`/`duration_ms` would appear as JSON fields in
a `toolUseResult`. They don't. The real mechanism:

Every task completion (both subagent completions and background-command completions) is a
`{"type": "queue-operation", "operation": "enqueue", ...}` line whose `content` field is a
**string** containing an XML-like `<task-notification>` block, which is then echoed a second time
as an ordinary `{"type": "user", ...}` line with the identical string in `message.content` (with
`origin.kind: "task-notification"`, `promptSource: "system"`). So this is text to parse with a
regex or a small XML/tag scanner, not a JSON field to look up.

Confirmed shape for a **successful agent completion**:

```xml
<task-notification>
<task-id>a5d73dc222fd0e42b</task-id>
<tool-use-id>toolu_01MGxQ5MFsPjPyTYVpK1pqD5</tool-use-id>
<output-file>/private/tmp/.../tasks/a5d73dc222fd0e42b.output</output-file>
<status>completed</status>
<summary>Agent "Explore openclaw-journey repo and its blog post" finished</summary>
<note>...</note>
<result>... the agent's full report text ...</result>
<usage><subagent_tokens>29668</subagent_tokens><tool_uses>8</tool_uses><duration_ms>40765</duration_ms></usage>
</task-notification>
```

Notable gotcha: everywhere else, duration is `durationMs` (camelCase, see above). Here it's
`duration_ms` (snake_case) - the naming convention is not consistent between the JSON transcript
fields and this embedded text block. Do not assume one casing convention project-wide.

The `<usage>` block is **conditional on success**. A `status=failed` completion (confirmed via a
real rate-limit failure) has `<summary>` but no `<result>` and no `<usage>` at all - there is
nothing to reconcile for a failed agent run, which makes sense but must be handled as a real case
(zero usage, not a parse error) rather than assumed away.

Background-*command* completions (`bash`/Monitor task notifications, task-id prefixed `b` rather
than an agent's hex ID) use the identical `<task-notification>` tag shape but never carry
`<result>` or `<usage>` at all - only `<status>` and a plain-text `<summary>`. So "does this
notification have a `<usage>` block" is the actual discriminator between an agent completion
worth reconciling and a background-command completion that isn't, not the task-id shape (which
is an implementation detail and shouldn't be relied on).

### Reconciliation does NOT hold under naive summing - confirmed, unresolved

This is the check the design doc's own "Ingest correctness" verification step exists to run, and
it fails as of this writing. For the real agent above (`<subagent_tokens>29668</subagent_tokens>`),
summing that same agent's own transcript file (`agent-a5d73dc222fd0e42b.jsonl`) gives:

- `input_tokens + output_tokens` across all its assistant turns: **331** - 90x too low
- adding `cache_read_input_tokens + cache_creation_input_tokens` on top: **232,036** - 7.8x too
  high

Neither simple combination lands anywhere near 29,668. The likely culprit for the second figure:
`cache_read_input_tokens` on turn N of a multi-turn run reflects a re-read of the *entire*
accumulated context up to that point, not an incremental delta - so summing it across turns is
correct for cost purposes (each turn really is billed for that read) but wildly overcounts if
`subagent_tokens` is meant to represent something like distinct content processed rather than
cumulative billed reads. That's a plausible explanation, not a confirmed one.

**Consequence for the ledger (B1): store the computed weighted cost and the reported
`subagent_tokens` figure side by side, never collapse them into one number, and surface the gap
rather than hide it.** Presenting an unreconciled cost figure as if it were validated would be
exactly the failure mode the design doc warns about ("nothing downstream can be trusted"). Root-
causing the actual formula behind `subagent_tokens` is unresolved and out of scope for this pass.

## Open items before B1 ingest code is trusted

1. ~~Confirm whether `effort` is present, and under what conditions.~~ Done 2026-09-12: present,
   top level, optional. See the assistant-lines section.
2. Confirm whether summing `iterations[].{input,output,cache_*}_tokens` reconciles with the
   top-level `usage` object, or whether one is a subset/rollup of the other.
3. Build the synthetic fixtures (hand-written, never derived from a real transcript per the
   design doc's publishability rules) that exercise every line type and field documented here,
   including a successful and a failed `<task-notification>`.
