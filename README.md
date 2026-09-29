# Loom

[![CI](https://github.com/azva-co/loom/actions/workflows/ci.yml/badge.svg)](https://github.com/azva-co/loom/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27-blue.svg)](go.mod)

An asset lifecycle and cost/routing engine for [Claude Code](https://claude.com/claude-code)
that needs no instrumentation, no hooks, and no write access to anything you own. It reads the
session transcripts Claude Code already writes, and never touches a file it did not create itself.

**Status: pre-1.0, under active development.** Ingest, ledger and reporting; asset discovery,
selector and MCP server; the policy table and generated agent definitions; per-lane filtering;
advisor proposals and the loop that re-measures after one is applied; context occupancy; promotion
rules over memory stores. See [`docs/design.md`](docs/design.md) for the full design and current
phase.

## Why

Claude Code accumulates durable assets - scratch drafts, memory, skills, plans, hooks, agents,
workflows, plugins - and nothing tracks which are used, which have gone stale, what each one costs,
or which combination fits a given task. The predictable result is skills nobody invokes, plans
nobody finishes, hooks that fire every session for no reason, and model selection by guesswork.

The data to answer all of it already sits on disk. Every session writes a transcript carrying the
model, the token usage by kind, tool calls, denials and corrections - and nobody reads it.

**The obvious next step is a tool that acts on what it finds: retire this, rewrite that hook, edit
this rule.** Loom is not that tool, on purpose, and has refused to become it five separate times
while its design was under review:

| An "active steward" would | Loom does instead |
|---|---|
| Edit or delete a stale skill | Propose it as a reviewable diff. Never write it |
| Rewrite a hook it judges wasteful | Report what the hook costs. You decide |
| Auto-apply a cheaper model once confident | Propose the pin, with its evidence and sample size. `loom policy set` is one command away, never automatic |
| Compact or consolidate your own files | Nothing. Recall and rewriting a human's own record is out of scope, permanently |

Everything Loom writes to lives in its own SQLite ledger and its own generated-output directory.
Nothing else. A tool in this space once shipped a version that deleted users' hand-written hook
entries; that class of failure is designed out here, not carefully avoided.

## How it works

1. **Ingest.** Read every session transcript under `~/.claude/projects`, retroactively over
   whatever history already exists, then incrementally as new lines are appended. No hooks, no
   instrumentation - the measurement layer needs none, because the data already exists.
2. **Store, as derived metrics only.** Token usage, tool calls, denials, timestamps, byte counts,
   file paths. Never message content. A planted-secret test in CI enforces this on every change,
   not just on the day it was written.
3. **Recommend, never apply.** Cost by model and by agent type, which assets a task actually
   needs, which memory files have drifted or gone stale, which policy would pay for itself. Every
   answer carries its own sample size, so a figure backed by one run never renders identically to
   one backed by fifty.
4. **Surface findings as proposals.** Structural, evidence-backed, capped at 20 pending at once, and
   deduplicated so a dismissal survives until the underlying facts actually change. You act on them,
   or don't - `loom propose apply` covers only what touches Loom's own state, and reverts in one
   command.

## Install

The repo is private for now (see "Status" above), by deliberate choice, not oversight - going public
is gated on at least one evidence-based proposal (a model pin, a retirement, a regression) actually
firing on real data, which hasn't happened yet. See [`docs/design.md`](docs/design.md), "Going
public", for the reasoning. Until then, `go install ...@latest` needs read access to the repo and
`GOPRIVATE` set - it does not work for a stranger, and does not work for the owner either without
both:

```sh
GOPRIVATE=github.com/azva-co go install github.com/azva-co/loom/cmd/loom@latest
```

Or build from a clone, which needs neither:

```sh
git clone https://github.com/azva-co/loom
cd loom && go build -o ~/go/bin/loom ./cmd/loom
```

Optionally register the MCP server with Claude Code, so a session can query its own cost history:

```
/plugin marketplace add azva-co/loom
/plugin install loom@loom
```

The plugin does not contain the binary and does not fetch one - install it first. See
[`plugin/README.md`](plugin/README.md).

## Quick start

```sh
loom report     # ingest ~/.claude/projects, print a cost/usage report
loom status     # what Loom knows, how stale it is, and what it cannot answer
loom advise "task text"   # recommend which skills/agents/plans fit, plus a model/effort choice
```

## Commands

**Report** `loom report` `loom status` `loom context`
**Recommend** `loom advise` `loom policy`
**Act on evidence** `loom propose` `loom propose apply` `loom propose dismiss`
**Integrate** `loom serve`

`loom serve` runs the MCP server on stdio, exposing one tool: `get_recommendation`.
Everything else above is CLI-only - a session that needs it runs the command directly.

## Supported

| | |
|---|---|
| OS | Linux (CI), macOS (daily use). Pure Go, no CGo - Windows should build, not yet verified |
| Go | 1.27+ |
| Transport | stdio only. No daemon, no socket, no listening port |

## Safety

- **No content stored, ever.** Only derived metrics and identifiers - a byte count, a model name, a
  file path. Never message content, enforced by a test that greps the whole database for a planted
  secret.
- **No network egress** from the core, as an invariant, not a default.
- **Never writes to a file it did not create.** Its own SQLite ledger and its own generated-output
  directory. Nothing else, ever - see the "Why" table above.
- **The ledger stays on the machine that produced it.** Never synced, committed, or backed up
  anywhere Loom controls.

Full threat model in [SECURITY.md](SECURITY.md).

## Docs

| | |
|---|---|
| [`docs/design.md`](docs/design.md) | The design, the constraints, and the build history. Read first |
| [`docs/transcript-schema.md`](docs/transcript-schema.md) | The transcript format Loom ingests |
| [SECURITY.md](SECURITY.md) | Threat model, and what "no content stored" actually means |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Development, review, and merge requirements |

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
