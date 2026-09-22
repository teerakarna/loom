# Test fixtures

Every fixture here is synthetic and hand-written. **No fixture may be derived from a real Claude
Code transcript, even redacted** - redaction of a large corpus is not verifiable, invention is.
See `docs/design.md`, "Publishability rules", and `docs/transcript-schema.md` for the format
fixtures should exercise.

## Manifest

Every file below is declared hand-written. `TestFixtureManifestIsComplete` fails if a fixture
exists that is not listed here, so adding one requires making the claim explicitly rather than by
omission.

| Fixture | Exercises |
|---|---|
| `synthetic-session.jsonl` | A session transcript: assistant lines with usage, a tool denial, user feedback, a task-notification |
| `synthetic-occupancy.jsonl` | B7b: `tool_use`/`tool_result` pairs (string and list content), an orphaned `tool_result` with no matching `tool_use` in this file, and a `compact_boundary` record |
| `synthetic-artifact-usage.jsonl` | B7a (#39): a `Skill` tool_use, `Read`/`Edit` tool_use blocks on the same file_path, and decoys - a skill name and a file path appearing only in plain message/tool_result text (a `Bash` command and its result), which must never count as usage |
| `multiline-response.jsonl` | One API response written across several lines, each repeating the same `usage` - the shape behind the 2.12x over-count bug |
| `subagents/agent-synthagent0001.jsonl` | A subagent transcript |
| `subagents/agent-synthmeta01.jsonl` | A subagent transcript carrying an `effort` field |
| `subagents/agent-synthmeta01.meta.json` | The companion file carrying `agentType` |
| `subagents/agent-synthnometa.jsonl` | A subagent transcript with **no** companion, so the unattributable path is covered |

## What the hygiene check does and does not prove

It checks for the **markers** of real data: absolute home paths, email addresses, a size ceiling,
and very long unbroken strings of the kind a real `thinking` signature produces.

It does **not** verify provenance. Nothing can prove from content that a file was written by hand
rather than derived from a real transcript. The check is named for what it actually does. The
manifest above is the real claim, and it is reviewable in a diff.
