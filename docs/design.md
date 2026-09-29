# Loom - an asset lifecycle and routing engine for Claude Code

Status: design agreed 2026-09-11.
Licence: Apache-2.0. Repo: personal GitHub, private until v0.1.0, then public.
Target: full open source project, distributed via the Claude Code plugin marketplace.

**B1 done (2026-09-11):** ingest, ledger, `loom report` - working, tested, run against real
history. Two real findings along the way, both in `docs/transcript-schema.md`: the subagent
completion record is an XML-like `<usage>` block inside a `queue-operation` line's text content,
not JSON fields, and does not reconcile with a naive sum of the agent's own transcript; and
`message.model` can be the literal string `"<synthetic>"` (a locally-injected status/error line,
always zero usage) - both handled, tested, documented.

**B2 done (2026-09-12):** asset discovery (skills, agents, plans, hooks, per-project memory -
`internal/asset`), selector (`internal/selector`), MCP server over stdio exposing
`query_ledger`/`get_recommendation`/`list_proposals`/`record_outcome` (`internal/mcp`), and
`loom advise`/`loom serve` CLI commands. Two real findings running `loom advise` against this
machine's actual skills: several real skills have no YAML frontmatter at all (just a `#` heading) -
fixed with a first-heading fallback for the description signal; and naive word-overlap scoring
without stopword filtering produced false-positive matches on shared function words ("the", "a",
"and", "for") - fixed by filtering a stopword list before scoring. `list_proposals` correctly
returns empty - nothing writes to that table until B5.

**B3 done (2026-09-12):** policy table, model pinning, generated agent definitions. Shipped in
four slices:

- **B3a** - `runs` now records `agent_type` and `effort`. Neither existed, so per-agent-type policy
  had nothing to key on. `agentType` lives only in the `.meta.json` companion beside each subagent
  transcript; `effort` is top level on assistant lines, not inside `message`. This closed a standing
  open item in `docs/transcript-schema.md`.
- **B3b** - `loom report` gained per-run cost, cost concentration, a by-agent-type breakdown, and
  the most expensive runs (closes #10). Totals alone invert the real ordering when run counts
  differ, which the policy engine would otherwise have inherited.
- **B3c** - policy resolution per agent type: stored, then evidence meeting `MinSampleSize` (20),
  then a shipped default. Every `Decision` carries `Source` and `SampleSize`, so constraint 11 is
  enforced by type rather than convention. `loom policy` shows it; `set`/`unset` give the
  deliberate override and its one-command revert.
- **B3d** - `loom policy render` writes agent definitions into Loom's own directory, never a
  client's. A decision resting on a shipped default is not rendered at all.

**The evidence path is deliberately unreached.** No agent type on a real corpus meets the 20-run
threshold (Explore has 14, fork has 4), so everything correctly resolves to a labelled default and
says what is still needed. That is the agreed behaviour, not an unfinished implementation.

Two findings from dogfooding, both fixed: usage was summed per transcript line when one API
response spans several lines, overstating cost by 2.12x (#6); and `UpsertPolicy` failed on any
ledger predating the `UNIQUE` constraint on `agent_type`, because SQLite cannot add a constraint
via `ALTER TABLE` and every unit test built its table fresh.

Issue #9 (the headline figure is uninterpretable and hides that 75% of weighted cost is cache
reads) was moved to B4 rather than held against B3: it is report presentation, not correctness, and
nothing in B3 depends on it. The B3 milestone is closed.

**B4 done (2026-09-13): lanes.** Runs are tagged with the project directory their session ran in,
taken from the transcript path. `loom report` gained a by-lane breakdown and a `--lane` filter, and
`loom status` reports lanes seen plus how many runs could not be placed in one.

Notable for what it did *not* ship. B4 arrived as a six-field manifest with a four-position
write-enforcement dial and left as one column, one filter and a display helper:

- **Write enforcement cut.** `scope`, `paths` and `promote_to` all policed what a session may touch.
  Loom has no hook into writes, constraint 8 forbids it writing to human-authored files, and
  enforcing would need a blocking `PreToolUse` hook - the blast-radius category, and a
  contradiction of "measures and recommends, the human applies".
- **The manifest cut.** The lane is already in the transcript path, so labelling needs no declared
  input. Labelling by path is a *description* and claims nothing; inferring a boundary would be a
  *decision*, which is the path-guessing this doc forbids by name.
- **Hard ledger separation deferred.** It is the only part that genuinely needs a manifest, and
  filtering one local ledger turned out to be enough. Constraint 12, not separation, is what makes
  the ledger safe on a machine mixing personal and work lanes - a correction recorded in the Lanes
  section rather than quietly fixed.

Also shipped alongside: `loom status` (#21), a cost-by-token-class breakdown so the headline figure
explains itself (#9), and `loom init` cut from the CLI list (#22) because it had no job the tool did
not already do.

B5 (advisor proposals for promotion and retirement) not started. Ledger-backed coordination was
**cut on 2026-09-12** rather than deferred:
it breaks constraint 7 by making sessions depend on Loom being installed, and it invents a mechanism
rather than optimising an existing one. Full reasoning under "Coordination between lanes: rejected".

Loom never reads or writes any external coordination state. Asset discovery scans skills, agents,
plans and per-project memory only, so a file-based exchange convention elsewhere in `~/.claude/` is
neither ingested nor interfered with, and its content never reaches the ledger (constraint 6).

**Pick this up on a personal machine.** Everything below is generic by construction, with no
employer context in it. Build it on personal hardware, on personal time, under personal accounts,
and keep it that way: the codebase must contain nothing derived from any employer's data, which is
both an IP-hygiene position and a hard requirement for publishing at all.

---

## Problem

Claude Code accumulates durable assets: scratch drafts, memory, skills, plans, hooks, agents,
workflows, plugins. Nothing knows which of them are used, which have gone stale, what each one
costs, or which combination fits the task at hand. The predictable result is skills nobody invokes,
plans nobody finishes, hooks that fire every session for no reason, and model selection by guesswork.

Meanwhile the data to answer all of it already sits on every user's disk and nobody reads it.

## The enabling fact

Claude Code writes a transcript JSONL per session under `~/.claude/projects/<slugged-cwd>/`. Each
assistant turn carries:

- `message.model` - which model actually served the turn
- `message.usage` - `input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`,
  `output_tokens`, plus service tier
- `effort` - reasoning effort
- `timestamp`, `durationMs`
- every `tool_use` block with its name and full input
- `toolDenialKind` where a tool call was denied
- `userFeedback` where the user corrected something
- `isSidechain` to distinguish delegated turns

Subagent runs get their own files at `<session-id>/subagents/agent-<id>.jsonl` (a workflow's own
agents nest a level deeper, and that directory also holds a non-transcript `journal.jsonl` - see
`docs/transcript-schema.md`, "Location"), and task-completion notifications in the parent transcript
record `subagent_tokens`, `tool_uses` and `duration_ms`, which gives an independent figure to
reconcile computed costs against.

So the measurement layer requires **no instrumentation at all**. Ingest is retroactive over whatever
history a user already has, then follows. This is what makes the tool useful on the day it is
installed, and it is why hooks are an optional extra rather than the foundation.

## What Loom does

Builds a local ledger of what you actually do. Reports where cost and rework go. Recommends which
combination of features to reach for on a given task. Proposes asset promotions and retirements as
reviewable diffs.

It measures and recommends. The human applies. That split is deliberate and permanent for anything
with blast radius.

---

## Design constraints, non-negotiable

The tool ships to strangers whose setups look nothing like the author's. Every one of these exists
to stop the design collapsing into one person's habits.

1. **Useful at n=0.** A new user with no history benefits immediately. Ship defaults reasoned from
   first principles; personalise as evidence accrues. Never require a corpus to be useful.
2. **Discover, never assume.** Find assets by scanning the standard Claude Code locations and the
   current project. No required layout, no required naming convention, no assumption that any given
   asset type is even in use.
3. **Schema-tolerant.** Read whatever frontmatter exists, treat missing fields as unknown, never
   rewrite a user's file to fit a preferred schema. Several skill frontmatter conventions exist in
   the wild; that is fine and not Loom's business to unify.
4. **No deployment-tool assumptions.** No dotfiles manager, no particular shell, no config
   framework. Install via `go install`, a Homebrew tap, a release binary, or the plugin.
5. **Boundaries are optional.** Default is one scope and zero configuration. Separation (work vs
   personal, or per client) is opt-in for those who need it, never a premise.
6. **Privacy by construction.** The ledger stores derived metrics and identifiers only, never
   message content. No network egress from the core, as an invariant. This is the property that
   makes it safe to point at transcripts containing confidential work.
7. **Degrade, never block.** If the engine is absent, stopped or broken, every integration point
   returns cleanly and the session proceeds unaffected.
8. **Never write to human-authored files.** Loom writes to its own database, its own generated-output
   directory, and proposal files. Nothing else. Settings files are read-only, always. A tool in this
   space once shipped a version that deleted users' hand-written hook entries; that class of failure
   gets designed out, not carefully avoided.
9. **Asset-derived text is data, never instructions.** A skill/agent/plan's name, description, or
   heading is read off disk and re-served verbatim through `get_recommendation` into whatever session
   called it - that is untrusted content re-entering another agent's context, the same shape of
   problem as Candor's "all ingested telemetry is untrusted" rule. Loom cannot force a downstream
   client to treat it as data rather than commands, but it bounds the blast radius: every such field
   is length-capped, and every tool that returns one says so in its own schema. A cheap heuristic
   flag on obviously injection-shaped text (`suspicious`) is surfaced alongside a match - advisory
   only, per constraint 1's whole ethos, never a filter. Full detection is not attempted: phrase-based
   classifiers are gameable and a false sense of security is worse than an honest gap.
10. **Every accelerator ships with its brake, and nothing grows without a bound.** Any mechanism
   that can act, spend, or generate has its limit defined and enforced in the same change that
   introduces it, never in a later phase. A brake added afterwards is not a brake: the window it
   was missing is exactly the window the thing ran unattended.

   This bites hardest for a tool whose entire purpose is reducing accumulated material. A mechanism
   that answers "too many assets to keep track of" by producing more assets has made the
   problem worse while appearing to help. So anything Loom generates states its retention rule up
   front: how many, for how long, overwritten or appended.

   Concretely, and these are current gaps as of 2026-09-12, not solved problems:
   - `record_outcome` appends to `events` with no cap. Needs a bound, or a documented reason the
     unbounded growth is acceptable.
   - B5's proposals need a generation limit and dedupe. An advisor producing proposals faster than
     a human accepts them recreates exactly the fatigue this tool exists to reduce.
   - The pre-compact hook (see "Integration") writes a resume pointer. It must **overwrite**, one
     per session, never append, or a context-relief mechanism becomes a context-consuming one.

11. **Say which numbers are evidence and which are assumption.** A figure derived from one run must
   not render identically to one derived from fifty. Recommendations carry their sample size, and
   the selector states when it is using a shipped default rather than measured history (see "Cold
   start"). The same discipline applies to anything the ledger reports: an unreconciled figure, a
   default, and a measured median are three different kinds of claim and are labelled as such.

   You can reason from a model of the world that is not yet backed by data, and often you have to.
   What is not allowed is losing track of which is which, because every downstream decision inherits
   that confusion silently.

12. **The ledger is machine-local, and stays that way.** It is never synced, committed, backed up to
   a shared location, or checked into a dotfiles manager. A rule, not an accident of where the file
   currently happens to sit.

   Constraint 6 keeps message content out. It does not keep *identifiers* out, and on any corpus
   that matters those identify real work: full transcript paths encode project directory names, and
   discovered assets carry their names and descriptions. Keeping the ledger on the machine that
   produced it is what makes Loom safe to point at a sensitive corpus at all.

   Corollary for anything published - docs, examples, issues, screenshots, write-ups: it comes only
   from a corpus you are willing to publish from. Loom cannot know which of a user's machines that
   is, and does not guess. The discipline belongs to whoever is publishing.

   This is deliberately *not* a work-versus-personal rule. Whether a user draws that distinction at
   all is theirs to decide (constraint 5); this says only that wherever a ledger is written, it
   stays there.

> **A note on these numbers.** Constraints are cited by number from Go comments, CONTRIBUTING.md and
> commit messages. Append new ones; never insert into the middle, which silently invalidates every
> existing reference.


---

## The model

Eight asset types on two axes. The axes are what make the promotion rules principled rather than
a matter of taste.

**Axis 1, decision authority at trigger time.** Who decides, and how binding it is:

```
Scratch  -> Memory -> Skill -> Plan -> Agent -> Hook -> Workflow
(ad hoc)    (fact)   (proc)   (intent) (delegated) (deterministic) (code)
```

`Plugin` is not a peer on this axis. It is packaging over any combination of the others.

**Axis 2, cost shape.** Three sub-costs, kept separate because they fail differently:

- **Tokens.** Context, and any LLM-backed hook, which pays a model call on every fire. Agents
  dominate, and model choice there is the only lever with roughly a 5x spread.
- **Wall clock and network.** Any command hook doing I/O, paid on every trigger, almost always
  unmeasured.
- **Blast radius.** Hooks that deny or rewrite tool calls. Cheap in tokens, expensive when wrong.

### Promotion rules

Shipped as a versioned data file the user can tune, not hardcoded prose. Every decision records the
`criteria_version` that produced it.

| From | To | Criteria |
|---|---|---|
| Scratch | Plan | Records intent plus decisions with a defined end state, not merely a record of a conversation |
| Scratch | Skill | The *shape* recurs (handover, status update, escalation) independent of its subject |
| Context | Memory | A fact, not a procedure; useful in a future session; needs no judgement to apply |
| Memory | Skill (procedure) | Has steps, needed twice or more, and you can write a description that genuinely discriminates when to invoke it. If the trigger phrase will not come, it is not ready |
| Memory | Skill (reference) | Domain-scoped rather than project-scoped. Memory loads only for its own project, so a cross-project fact needs a skill to be reachable everywhere. This is why factual skills with no steps are legitimate, and why "a skill must be a procedure" is the wrong rule |
| Skill | Agent | Delegable end to end with a bounded tool set, and costly enough that pinning a cheaper model than the orchestrator pays for itself |
| Skill | Hook | The trigger is structurally detectable (tool name, regex, glob) and a probabilistic miss is expensive. If judgement cannot reduce to a pattern but the rule must always fire, use an LLM-backed hook and accept the per-fire cost. Do not force a bad regex |
| Plan | Workflow | Steps repeat across a list of similar items, each independently resumable, and the shape held stable across two or more real executions |
| Workflow | Skill (reverse) | Once the orchestration shape itself is reusable, wrap it in a skill whose only job is when to reach for it |
| Any set | Plugin | One cohesive concern, needs independent enable/disable/version, or bundles an MCP server |

**The single most useful rule:** a deterministic hook requires a structurally checkable condition.
If you cannot write the condition, it is not a hook. Unconditional hooks that fire every session
just to print a reminder are the common anti-pattern this catches, and they are pure cost.

### Scratch

Loosely defined on purpose. A scratch area is wherever someone keeps ad hoc, on-the-fly drafts:
messages to colleagues, meeting notes, half-formed thinking. Usually untracked, usually disposable.

Loom treats it as a **read-only inbox**. No imposed naming convention, no template, no directory
structure, and it never writes there. Its only interest is spotting when something has outgrown
scratch: a draft that is really a plan, or a message shape written for the fourth time that should be
a skill. Path is configurable, discovery is best-effort, and the whole feature is skippable.

### Lanes

**Rescoped 2026-09-13** against how lanes are actually used, rather than how this doc originally
imagined them. The original spec is preserved at the end of this section, because the gap between
the two is the useful part.

A lane is a directory you launch a session from, with its purpose defined by convention and memory.
That looseness is deliberate: in real use any lane may review a PR from any repo, so lanes overlap
constantly, and the overlap is a feature rather than a problem to design out.

**Overlap and separation happen at different levels, which is what makes this tractable:**

- A *session* belongs to exactly one lane: the directory it was launched from. Unambiguous.
- A *lane* may associate with any number of repos and topics, and a repo may belong to many lanes.
  Overlap is free, because associations are metadata and nothing is partitioned by them.
- A *run* is attributed to its session's lane, never to the repos it touched. So the ambiguity of
  "which lane owns this repo" never has to be answered.

#### Filtering, not a manifest

**Decided 2026-09-13: B4 ships no manifest.** A run's project directory is already in its transcript
path, so tagging and filtering by it costs nothing and needs no configuration at all.

What a manifest would add, and why none of it justifies the file yet:

| Want | Needs a manifest? |
|---|---|
| Tag runs by project directory, filter reports by it | **No** - already in the path |
| A friendly lane name instead of a slugged path | Yes, but purely cosmetic |
| Hard ledger separation between boundaries | **Yes** - a decision, not inferable |

Hard separation is the only one that genuinely requires declared input, because a boundary is a
*decision* rather than a *description*, and inferring a decision from a path is the path-guessing
this doc forbids by name. Labelling by path is fine: it describes where a session ran, and claims
nothing.

**A correction worth keeping.** An earlier version of this section argued that `boundary` was what
made Loom "safely installable" on a machine mixing personal and work lanes. That was wrong.
Constraint 12 is what makes it safe: the ledger stays on the machine that produced it. Mixing lanes
in one local ledger on a machine that already holds both is an *analysis* limitation, not a safety
one. You lose the ability to ask what work cost versus personal. Useful to have, not load-bearing,
and the difference matters when deciding what to build.

So the separation dial reduces to two positions today, with the third available later if a real need
appears:

| Strength | Mechanism | Status |
|---|---|---|
| None | one ledger, no lane recorded | today |
| Soft | one ledger, runs tagged by project directory, filterable | **B4** |
| Hard | one ledger per declared boundary | deferred; needs a manifest, no evidenced need yet |

#### Stated limitation

A run is attributed to the directory its session ran in, never to the repos it touched. Reviewing a
work PR from a personal lane files that run under the personal lane. That is a description of where
the work happened, which is true, rather than a claim about what the work was about, which would not
be.

#### What was cut from the original spec, and why

The original manifest carried a `scope` dial: `pinned` (writes only to one repo), `scoped`,
`themed` (writes outside the lane need confirmation), `open`. Every one of those is **write
enforcement**, and Loom cannot do it. It has no hook into what a session writes, constraint 8
forbids it writing to human-authored files, and enforcing would require a blocking `PreToolUse`
hook - the blast-radius category, and a direct contradiction of "it measures and recommends, the
human applies".

`promote_to` and `paths` went with it: both existed to serve enforcement.

What replaced the dial is a dial about *data separation*, which Loom genuinely owns. Same idea of
"a range of strength", applied to something the tool can actually deliver.

<details>
<summary>Original lane spec, superseded</summary>

```toml
name       = "example-lane"
scope      = "scoped"           # pinned | scoped | themed | open
paths      = ["../some-repo"]   # what this lane may touch
topics     = ["deploys", "ci"]
promote_to = "../some-repo/plans"
boundary   = "default"          # opt-in separation
```

Scope dial, tight to loose: `pinned` one repo writes only there; `scoped` a named set of repos;
`themed` topic tags with writes outside needing confirmation; `open` scratch with no writes outside
the lane directory.

</details>

**Coordination between lanes: rejected 2026-09-12.** This originally specified append-only ledger
tables (`messages`, `presence` with a TTL, `tasks`) to replace a shared markdown coordination file.
It is not being built, and the reasoning is worth keeping rather than leaving as silence.

**It breaks constraint 7.** "If the engine is absent, stopped or broken, every integration point
returns cleanly and the session proceeds unaffected." Measurement degrades gracefully: no Loom, no
report, session unaffected. Coordination cannot. If sessions coordinate through Loom's SQLite and
Loom is not installed, they cannot see each other at all. That is a hard dependency, not a
degradation, and it is the one category of feature this constraint structurally forbids.

**It breaks the thesis.** The enabling fact above is that the data already exists and the
measurement layer needs *no instrumentation at all*. Coordination is the opposite: it invents a
mechanism Claude Code does not have and requires every session to opt into a protocol. That is a
category shift from optimising what exists to adding something new.

**It imposes one person's habits**, which the design constraints open by forbidding. A file-based
convention in someone's own home directory is inert to everyone else. Coordination tables shipped
in Loom push that opinion onto every user of the tool.

**Files are the more enduring choice here.** Every durable Claude Code asset is a file: skills,
memory, plans, CLAUDE.md, transcripts. A markdown/JSONL convention runs with the grain of the
platform, is greppable, debuggable without a CLI, and survives Loom being uninstalled or rewritten.
A table in one tool's private database does none of that.

**And it complements nothing.** Lanes feed the selector: declared scope informs recommendations.
Coordination would share a database with the rest of Loom and nothing else. A separate product
wearing the same binary.

The `coordination` table stays in the schema as an empty, unused artifact of the original design
rather than being dropped in a migration, but nothing reads or writes it. Anyone wanting
cross-session coordination should use a file convention outside Loom.

**Correction, same day.** An earlier version of this section argued the rejection partly on the
grounds that the author's own file-based exchange had "gone unused". That was wrong: it was inferred
from one machine, where the exchange happens to be quiet, and generalised to a practice spanning
several environments. On the author's work machine the exchange is in active, heavy use. The
rejection stands on the five arguments above, none of which depend on usage, and the usage argument
is withdrawn rather than quietly deleted.

---

## Architecture

Three layers. Nearly all logic sits in layer 1, so the parts wired into a client stay small and cheap.

### 1. Core engine - Go, single static binary

Go for stdlib coverage, cross-compilation to four platforms from one machine, and a single binary
with no runtime to install. macOS and Linux at v0.1.0.

**Ingest.** Streaming JSONL reader with per-file byte-offset checkpoints, decoding line by line so
memory stays bounded regardless of corpus size. Retroactive on first run, then follows changes via
fsnotify, watching *directories* rather than individual files to stay clear of platform FD limits,
debounced, with a slow reconcile sweep as the safety net for missed events. Event-driven rather than
polling: real-time reaction at near-zero idle cost.

**Ledger.** SQLite, WAL mode. Six tables:

- `assets` - one row per known asset instance, with type, path, status
- `events` - append-only; this is the ledger
- `runs` - one row per session or agent execution, with weighted cost and outcome
- `policies` - the model/effort/tools decision table
- `proposals` - pending recommendations with their evidence
- `coordination` - unused. Retained empty; see "Coordination between lanes: rejected" above

No content store and no full-text index. Recall of past conversation is a different problem and
explicitly not this tool's job. Expected size is single-digit megabytes.

**Selector.** Given a task descriptor, returns a recommended combination: which skills to surface,
which agent type at which model and effort, whether an existing workflow already covers it. Pure
code, no model call, and a transparent scoring function over a handful of features (referenced
paths, tool verbs, skill description match, similarity to previous task descriptors and their
measured outcomes). Deliberately **not** machine-learned: at realistic personal data volumes there
is nothing to learn from, and a transparent function is debuggable, fast, and honest about its
reasoning.

**Serve.** Local only, stdio. `loom serve` runs the MCP server over stdio and blocks until the
client disconnects - no daemon, no socket, no TCP, no network egress. A client spawns it as a
subprocess per its own MCP server config, one process per session.

### 2. Integration - MCP first

**An MCP server over stdio is the primary surface.** It works in any MCP client, needs no edits to
anyone's settings file, and installs in one line. As specified here it exposed four tools; narrowed
to `get_recommendation` alone - see "MCP server shape, narrowed further", B7's open items.

**Hooks are optional enhancements**, documented but never required, because on some setups the
settings file is owned by another process and hook registration is a permanent manual step. Users who
want them get five, none LLM-backed, each with a stated latency budget:

| Hook | Job | Budget |
|---|---|---|
| session start | Lane briefing plus opening recommendation | < 20ms |
| prompt submit | Per-task recommendation | < 20ms |
| stop / subagent stop | Prompt cost attribution ahead of transcript flush | < 5ms |
| pre-compact | Write a resume pointer: table of contents plus runnable queries, never content | < 50ms |

**Note on pre-compact (2026-09-12):** this hook is a pointer-writer, not a gatekeeper, and that's a
permanent design decision, not a placeholder to revisit. It pulls from the ledger - most expensive
runs this session, denied/corrected tool calls, assets touched - and writes that as the resume
pointer; `session start` reads it back as the briefing. It never decides what Claude Code's own
compaction keeps or drops. Same reasoning as "It measures and recommends. The human applies." above:
a hook that prunes or rewrites context has the same blast-radius shape as a hook that denies or
rewrites tool calls (Axis 2, cost shape) - cheap-if-wrong stays the only kind of hook Loom ships.
Not yet built - B2 shipped selector + MCP server only, no hooks.

**CLI** for humans: `report`, `status`, `advise`, `policy`, `serve`, and `lane` once B4 lands.

`loom init` was listed here and is **cut (2026-09-13)**, not deferred. It was never implemented and
belonged to no phase, and on inspection it had no job: `ledger.Open` creates and migrates the
database on first use, and `report`/`advise` discover and ingest with no setup step. A command whose
only function is to do nothing visible is worse than its absence, because it implies a setup ritual
that does not exist. If a real need appears later, a lane manifest scaffold is the likeliest
candidate and belongs to B4.

`loom status` was added in its place, answering the question a new user actually has: is this
working, what does it know, and how current is it. It is read-only by design, because `report`
ingests as a side effect and a user checking freshness with `report` would be changing the thing
they were checking.

**Agent definitions are generated output.** Model pinning lives in agent frontmatter, rendered from
the `policies` table, so the policy is data and the files are a reviewable diff. Generated files land
in their own directory and never overwrite hand-written ones.

**One skill**, the Advisor, which reads proposals and walks the user through accepting or rejecting
them. A diff-and-decide pattern applied to assets.

### 3. Distribution

- `go install`, a Homebrew tap, and release binaries for darwin/arm64, darwin/amd64, linux/amd64,
  linux/arm64
- **A Claude Code plugin** for the marketplace. Plugins are cloned content and cannot carry a
  compiled binary, so the plugin ships the MCP server registration and a manifest, and resolves a
  binary the user installed themselves.

  This paragraph used to end "its install step fetches the matching platform binary and verifies it,
  refusing an unverified one rather than falling back", and listed hooks and an Advisor skill among
  the contents. All three were wrong by the time B6c was built: the hooks and the skill had been
  cut, and verifying a download needs signatures to verify against, which B6a has not shipped. Left
  here as a correction rather than edited away, because a doc that quietly rewrites its own promises
  is exactly what this project keeps telling other people not to do.
- Release integrity: goreleaser, SHA256 checksums, cosign keyless signing, SBOM per release. A tool
  that downloads a binary is a supply chain and should behave like one

---

## The self-tuning loop

Measure, propose, apply, re-measure. One human gate, at apply.

**Measured, all from data already on disk:**

- Weighted cost per run. Token kinds are not interchangeable: cached input is roughly an order of
  magnitude cheaper than fresh input, so the cost function weights by kind. Summing raw tokens would
  make every downstream recommendation wrong. Get this right first or nothing else means anything.
- Per `{agent type, model, effort}`: median tokens, duration, tool-call count.
- Rework proxies: tool denials, user corrections following a result, repeated near-identical tool
  calls, plan churn.
- Asset use counts and staleness.

**Cold start.** With no history the selector uses shipped defaults reasoned from first principles
(cheaper model for well-scoped retrieval and mechanical execution, stronger model for planning and
ambiguous synthesis) and says that is what it is doing. As runs accumulate, evidence displaces
defaults per agent type, each recommendation carrying its sample size. Personalisation earns its way
in rather than being assumed. This is the mechanism by which the tool moulds to a user instead of
imposing a shape on them.

**Proposals** are diffs against the policy table, each carrying sample size, effect size, and a
pointer back to the runs that justify it. Applying writes a marker so the next measurement window
compares against the previous one. That comparison is what makes this a loop rather than a report.

**Guardrails, because samples are small and quality regressions are quiet:**

- No proposal below a minimum sample size. Report effect size, never a bare mean.
- Never propose a cheaper model for an agent type whose measured rework rate is already elevated.
- Every applied change reverts to the prior policy version in one command.

### B5 scope, agreed 2026-09-13

#### Two kinds of proposal, and only one may ever be automated

The split falls straight out of constraint 8 and decides everything else:

| A proposal that touches | Loom may | Automation |
|---|---|---|
| **The user's files** - promote a scratch note to a plan, retire an unused skill | render a diff, nothing more | **Never.** Not once, not with permission. Loom does not write there at all |
| **Loom's own state** - pin a model in the `policies` table | apply it | Defensible: blast radius is confined to Loom's ledger, and `loom policy unset` already reverts it in one command |

"It measures and recommends. The human applies" stays absolute for anything outside Loom's own
database. Inside it, automation is a convenience over an action the user could already take, and it
is revertible.

#### Surfacing, in the order it earns

Loom has no terminal of its own. It is a CLI and an MCP server; Claude Code owns the keyboard and
the screen. There is no hotkey, no submenu and no TUI - a Loom that owned the terminal would be
competing with the thing it exists to observe. Three surfaces exist, and each earns the next:

1. **Pull.** `loom propose` lists pending proposals with their evidence. You ask, it answers, it
   exits. Zero intrusion, no new machinery.
2. **The MCP tool.** `list_proposals` already exists and returns empty. This is the most natural
   notification available: the user is already in a conversation, so a proposal can be raised when
   it is relevant rather than when a timer fires.
3. ~~**Session-start hook, optional.**~~ **Cut 2026-09-13**, on the evidence below. It was the third
   surface and it does not earn its place.

   - **It blows its own latency budget by 48x.** The table above budgets session-start hooks at
     under 20ms. Measured: 10ms warm, **960ms cold**. Session start is precisely when the binary is
     cold, so the realistic figure is the bad one.
   - **It would have nothing to say for months.** On a real corpus: the nearest retire proposal is
     90 days away *and* requires a skill to actually be deleted; the nearest pin needs 6 more runs
     of one agent type and 16 of the other. So the honest output at every session start, for weeks,
     is "0 proposals" - a line that costs context and says nothing.
   - **It would write at session start.** Generating proposals mutates the ledger. A hook that
     writes before the user has typed anything is a side effect in the wrong place, and a failed
     write there is worse than a failed read.
   - **A read-only version is circular.** Making it cheap means counting already-stored proposals
     without generating. But proposals only exist if something already generated them, so on any
     machine where the CLI has not been run it reports 0 forever. It would tell you about proposals
     that exist only if you already looked.
   - **The slot is contested.** A skill-maintenance hook already occupies SessionStart, and the
     lane briefing in the table above wants it too. Three claimants on one line of startup context.

   **`list_proposals` already does this better.** It surfaces a proposal when it is relevant to what
   the user is doing, rather than unconditionally at startup, which is better targeting at zero
   fixed cost. Push was the wrong instinct; pull-when-relevant is the right one.

   This is the failure constraint 10 names, caught before building rather than after: a mechanism
   that answers "too much to keep track of" by printing more at every session start has made the
   problem worse.

#### Automation controls live in the ledger, not a config file

`apply once` / `ask every time` / `apply every time` / `edit the value` is the right shape, and the
preference belongs in the ledger alongside `policies`, not in a new dotfile. Same reasoning that
cut the lane manifest: do not add a file when there is already somewhere to put it, and a tool whose
default is zero configuration should stay that way.

#### Bounds, per constraint 10, in the same change that adds the mechanism

- A cap on **pending** proposals. An advisor producing proposals faster than a human accepts them
  recreates exactly the fatigue this project exists to reduce.
- **Dedupe**: the same finding is not re-proposed on every run. A dismissed proposal stays dismissed
  until the evidence behind it changes, which is the same content-addressed idea as the fingerprint
  gate in Candor: re-raise on change, not on a timer.
- A cap on **auto-applies per window**, with every one recorded and revertible, for the Loom-internal
  kind only.

#### Build order

Pull first, MCP tool second, ~~session-start nudge third~~ (cut, see above), auto-apply last and
only for Loom-internal state. Each step is useful stopping there, and nothing later is required to
make anything earlier worth having.

**Auto-apply: rejected 2026-09-13, replaced by a one-command manual apply.**

The arithmetic kills it. A pin proposal is suppressed once a policy exists, so it can fire **at most
once per agent type, ever** - two times on the corpus this was measured against - and each firing
saves exactly one command. That does not justify preference storage (`apply once` / `ask every
time` / `apply every time`), a cap on auto-applies per window, and writes happening while nobody is
watching.

What the convenience was actually worth is not retyping a model name off a proposal. So
`loom propose apply <id>` does that, and nothing else: a person runs it, it writes only to Loom's
own policy table, it records the policy's source as `evidence` with its sample size, and
`loom policy unset` reverts it. "The human applies" stays literal rather than becoming a setting.

Apply is deliberately **CLI-only**, for the reason below: applying changes policy, and an assistant
can call an MCP tool without the human asking, so the terminal is where a decision that changes
state belongs. At the time this was written, `list_proposals` and `dismiss_proposal` were also
exposed over MCP, since listing is read-only and dismissing only hides a suggestion. Both later moved
to CLI-only too, for a different reason - see "MCP server shape, narrowed further" under B7's open
items.

Status: pull shipped (B5a), MCP surface shipped (B5b), nudge cut, auto-apply rejected and replaced
by `loom propose apply` (B5d). **B5 complete as scoped.**

### Loop closure, built 2026-09-13

Applying a policy currently ends the story. The doc above already says it should not: *"Applying
writes a marker so the next measurement window compares against the previous one. That comparison is
what makes this a loop rather than a report."* That marker does not exist, so the self-tuning loop
is currently a one-way trip.

**The asymmetry that makes this work.** Auto-*apply* was rejected, but auto-*revert* is a different
proposition and a more defensible one: applying moves you to an unproven state, whereas reverting
restores a known-good state you were already in and had evidence for. So the position is
**apply manually, verify automatically, revert automatically when the evidence turns** - which is
coherent in a way "apply automatically" never was.

What it needs:

- **A marker on apply.** `Apply` records what changed, when, and the baseline it was justified by
  (the median cost, tool count and rework rates behind the proposal). Attached to *apply*, not to
  auto-apply: a policy change needs verifying whoever made it, and manual apply is what exists.
- **A comparison window.** Runs for that agent type *after* the marker, measured the same way, so
  before and after are like for like.
- **A regression threshold**, stated rather than implied, and a minimum post-apply sample so a
  regression cannot be declared on two runs (constraint 11 applies to this judgement as much as to
  the original proposal).
- **A revert path that says why**, not merely that. "Reverted Explore to the shipped default: median
  cost rose 40% over 22 runs since the pin" is actionable; "reverted" is not.

**Honest limitation, stated up front.** This needs enough runs *after* an apply to mean anything.
With a 20-run threshold to propose in the first place, a comparable window afterwards is not quick.
It ships correct and quiet, like the evidence path in B3 - intended behaviour, not a shortfall.

**Built as a third proposal kind**, `revert_policy`, so it reuses the machinery that already exists
rather than adding a parallel one. A regression becomes a proposal with before/after evidence;
applying it removes the policy; and because the policy is gone, the original pin becomes proposable
again. The loop closes *and* re-opens, which is what makes it a loop.

**Automatic revert was considered and is not needed**, for a reason that only became clear once the
nudge was cut: **nothing triggers Loom unattended.** There is no daemon and no hook. "Auto-revert"
would mean "revert the next time you run `loom propose`" - at which point you are looking at the
proposal anyway, and applying it is one command. The automation would buy nothing that the surface
does not already give.

Thresholds, stated rather than implied: `MinPostApplyRuns` (10, deliberately below the 20 needed to
*apply* - the bar for returning to a known-good state is lower than for leaving it),
`RegressionCostRatio` (1.25), `RegressionReworkDelta` (0.2 per run). Rework is checked before cost,
because a cheaper model that gets things wrong is not a saving.

---

## Phasing

Each stage is independently useful. Stopping after any of them leaves something that works.

- **B1** Ingest, ledger, `loom report`. Read-only and retroactive. Answers "where does my spend
  actually go" from existing history on day one. This is the stage that proves the whole premise.
- **B2** Selector plus the MCP server. Recommendations only, advisory.
- **B3** Policy table, generated agent definitions, model pinning. The cost payoff.
- **B4** Lanes, rescoped 2026-09-13 down to what needs no configuration: tag each run with the
  project directory its session ran in, and let reports and policy filter by it. No manifest, no
  new file in anyone's repo. Write enforcement was cut (Loom cannot do it without becoming a
  gatekeeper) and hard ledger separation was deferred (it needs declared input, and filtering a
  single local ledger turned out to be enough).
- **B5** Advisor proposals for promotion and retirement.
- **B6** Signed releases, plugin packaging, and the decision to go public. Scoped in detail below.
- **B7** Context occupancy: what fills the window, how fast, and what compaction costs. Plus the
  three credibility issues the advisory half rests on. Scoped in detail below.

### B6 scope, agreed 2026-09-13

Four pieces, and they are not equally justified. Two are clearly needed, one is thinner than this
doc has been claiming, and one is a decision rather than a build.

#### B6a. Signed releases - needed, and coupled to publishing

goreleaser, SHA256 checksums, cosign keyless signing, SBOM per release, binaries for darwin/arm64,
darwin/amd64, linux/amd64, linux/arm64. None of it exists today: there is no `.goreleaser.yml` and
no release workflow.

A tool that asks people to download a binary is a supply chain and should behave like one. Note the
coupling though: nobody downloads a binary until there is a public release, so this is worth
building *as part of* going public rather than before deciding to.

#### B6b. Fixture-hygiene check - BUILT 2026-09-13

The publishability rules require this in CI before the repo goes public. It is worth building and
worth being honest about: **provenance cannot be verified from content.** Nothing can prove a file
was hand-written rather than derived from a real transcript.

What it can do is check for the *markers* of real data, which is a different and weaker claim that
should be made in those words:

- no absolute home paths (`/Users/<name>`, `/home/<name>`) - currently clean
- no email addresses, no bearer tokens (gitleaks already covers credentials)
- a size ceiling, since real transcripts run to megabytes and every current fixture is under 2.5KB
- a manifest in `testdata/README.md` naming each fixture and asserting it was hand-written, so the
  claim is at least recorded and reviewable rather than assumed

Naming it provenance would be the tool claiming more than its evidence supports, which this project
keeps telling other people not to do - so it is called hygiene.

Built as a Go test (`internal/ingest/fixtures_test.go`) rather than a separate CI job, so it runs
wherever `go test` does with no new machinery. Each check was verified to fail against a planted
violation rather than assumed to work: an undeclared fixture, a `/Users/<name>` path, a
600-character unbroken string, and a 60KB file were each caught.

#### B6c. The Claude Code plugin - BUILT 2026-09-13, thinner than this doc said

The plugin was specified as shipping "the MCP server registration, the optional hooks, the Advisor
skill and a manifest". Two of those four no longer exist:

- **The hooks.** The session-start nudge was cut on measurement, and no other hook was ever built.
  There are no hooks to ship.
- **The Advisor skill.** Described as reading proposals and walking the user through accepting or
  rejecting them. `list_proposals`, `dismiss_proposal` and `loom propose apply` already do exactly
  that, through surfaces that exist. A skill wrapping them would be a third way to do the same
  thing.

So the plugin would ship an MCP server registration and a manifest. That is not nothing - a one-line
install beats "edit your settings.json by hand", and marketplace presence is the distribution
channel. But it should be built as what it is, and the plugin README updated, rather than shipping
against a description that no longer matches.

**Built to that narrower description.** `plugin/.claude-plugin/plugin.json`, `plugin/.mcp.json`, and
a `.claude-plugin/marketplace.json` at the repo root so the repository is its own marketplace. The
README carries a table of what ships and what does not, including the two pieces that were specified
and then cut.

Three things came out of building it that the scope above did not anticipate:

- **The MCP command is a wrapper script, not `"command": "loom"`.** Claude Code launches an MCP
  server with the environment it was itself launched with. `go install` puts the binary in
  `~/go/bin`, which is on an interactive shell's PATH and is not on the PATH a desktop app inherits
  from the launcher - verified, not assumed. Registering the bare name works from a terminal and
  fails from the app, which is the worst available failure mode because it reads as a Loom bug.
  `plugin/bin/loom-mcp` checks `$LOOM_BIN`, then PATH, then where the documented install methods
  actually put it, and says so in words when it finds nothing.

- **The plugin does not fetch or verify a binary**, which is what "Distribution" below said it
  would. That needs signed releases to verify against and there are none (B6a). A fetch step with
  no signature to check would be the shape of supply-chain safety without the substance. It
  resolves a binary the user installed and is explicit that this is what it does.

- **`claude plugin validate --strict` exists**, is first-party, runs unauthenticated, and is now a
  CI job. It checks schema shape only: a manifest pointing at a command that does not exist, a
  wrapper that has lost its executable bit, and a version that disagrees with the server being
  shipped all passed validation while broken. Each was planted and confirmed to pass, which is why
  `internal/mcp/plugin_test.go` covers those three and the validator covers the schema. The two are
  complementary rather than redundant.

#### B6d. Going public - a decision, with prerequisites

Not a build. The prerequisites are already written down in the publishability rules:

- secret scanning green in CI - **done** (gitleaks, 2026-09-12)
- fixture-hygiene check green - **done 2026-09-13**
- a manual pass for real names in docs, README and examples - currently clean, checked 2026-09-13,
  and worth re-checking immediately before rather than trusting this line
- signed releases actually working - **B6a**
- **at least one evidence path has fired on real data.** Not a build, a precondition. Every
  evidence-driven behaviour in this tool - pin proposals, retirement, regression detection - has so
  far only run against synthetic fixtures and seeded demos. On a real corpus they all correctly
  report insufficient evidence, which is right, and which means the interesting half of the tool is
  unproven outside its own tests. Publishing something whose headline behaviour has never been
  demonstrated on real data would be the thing this project spends four blog posts criticising.

  This gate costs nothing to add and is satisfied by patience: keep it installed, let the corpus
  grow past a threshold, and watch one proposal appear for a real reason. The thresholds themselves
  (`MinSampleSize` 20, `StaleAfter` 90 days, `MinPostApplyRuns` 10, `RegressionCostRatio` 1.25,
  `RegressionReworkDelta` 0.2) are every one of them a reasoned guess, and cannot be calibrated
  without exactly that corpus.

There is also the owner's own standing rule that a repo goes public only for a concrete demonstrated
benefit, never to unblock a feature.

This section used to claim the benefit was that **the plugin marketplace requires a public
repository**, and called that the forcing argument. Checked against the documentation while building
B6c, and it is false: a marketplace can be private, and installs from it work for anyone with git
credentials to the repo. The owner installing Loom's own plugin on their own machines needs nothing
public at all. Public is only required for *strangers* to install without access being granted.

So the forcing argument is weaker than it was written to be, and the honest version is narrower:
going public buys distribution to people the owner does not know. That may well be worth it, but it
is a choice about who this tool is for, not a prerequisite dropping out of a tooling constraint. If
the answer is "for me, on my machines", a private marketplace already does the job and B6d has no
deadline.

#### Order

~~B6b first~~ **done**. ~~Then B6a, then B6c~~ - B6c went before B6a, because the discovery above
reverses their coupling: a private marketplace works today, so the plugin is usable now, whereas
signed releases only matter once there are strangers downloading binaries. B6a is therefore part of
going public rather than a prerequisite for the plugin.

Remaining: **B6a** if and when B6d is decided yes, and **B6d** itself, still gated on at least one
evidence path firing on real data. Both of the pieces worth having regardless of that decision -
B6b and B6c - are done.

#### The README's install command does not work, filed and fixed - BUILT 2026-09-23

Found on the first real trial on a second machine (AMC), filed as loom #57: `go install
github.com/teerakarna/loom/cmd/loom@latest`, the README's headline command, fails for everyone while
the repo is private, including the owner with valid credentials - `sum.golang.org` 404s on a module
it cannot fetch, and the proxy's fallback to a direct `git ls-remote` does not go through the normal
credential helper, so it prompts for a username with prompts disabled. The issue itself framed the
real question correctly: this is a symptom of "go public or stay private", not a bug to patch around
either way, so it stayed unimplemented pending a decision rather than getting a quick unprincipled
fix.

Checked directly against this section's own gate before deciding: `pin_model`/`retire_asset`/
`revert_policy` proposal counts and the `policies` table, on this machine's real ledger, all still
zero. The gate is unmet, so **stay private**, consistent with what this section already committed to
rather than a fresh call. README and `plugin/README.md` now lead with a private-install path
(`GOPRIVATE=github.com/teerakarna go install ...`, or build from a clone) instead of the command that
does not work, and say plainly that this is deliberate, not a stale README - it goes back to a
one-liner once B6d actually fires.

#### A pending proposal never withdraws itself - BUILT 2026-09-23

Issue #40, found while confirming #38: `UpsertProposal`'s dedupe rule is careful about two cases
(evidence unchanged, leave alone; evidence changed, re-raise) and silent about a third - evidence
gone entirely. A proposal whose facts stopped holding just sat there, pending, quoting numbers that
were no longer true, until a human dismissed something that was never wrong so much as stale. That
is the alert-fatigue failure mode this project criticises other tools for, arriving through the back
door of its own advisor.

New `ProposalWithdrawn` status, distinct from dismissed for the reason the issue gave: "the situation
changed" and "a human rejected it" are different events, and collapsing them into one status loses
the thing the ledger is for. `ledger.WithdrawStalePending` marks pending rows withdrawn when the
current generated set does not contain them; `propose.Store` calls it before its own upsert loop, not
after - `MaxPendingProposals` counts `status = pending`, so withdrawing stale rows first is what lets
a genuinely new proposal use the slot a withdrawn one just gave up, in the same `loom propose` run
rather than the next one. `Apply` now also refuses a withdrawn proposal outright, for the same reason
it already refused an applied one: acting on it would mean acting on evidence that no longer holds.

Dismissed and applied rows are untouched by the new mechanism - confirmed by a test that dismisses
one proposal, withdraws another, and checks each kept its own status rather than one leaking into the
other.

**Three real bugs found by `/code-review high` before merge, empirically verified, not just
theorised.** The most severe one undid the whole point of this section: `UpsertProposal`'s original
"evidence unchanged, leave alone" fast path did not distinguish withdrawn from dismissed, so a
proposal whose evidence is static content rather than a daily-drifting number - the four B7c
memory-finding kinds, `broken_link`/`unreachable_asset`/`filename_slug_drift`/
`promote_memory_duplicate` - could never return to pending once withdrawn even once, contradicting
this section's own stated point that a withdrawal, unlike a dismissal, is not meant to be sticky.
Fixed by treating a withdrawn row as equivalent to "no row" for the purposes of that fast path: it
revives on unchanged evidence rather than staying stuck, and - the second bug this surfaced - the
revival now also clears the same `MaxPendingProposals` cap a brand new proposal would, since without
that check a burst of revivals could silently exceed it. Third: the dedupe key `WithdrawStalePending`
took was a `"kind|subject"` string concatenation, and `subject` is a raw filesystem path that can
legally contain `|` on POSIX - replaced with `ledger.ProposalIdentity`, a struct key, collision-proof
by construction rather than by the absence of an unlucky filename. `WithdrawStalePending` also moved
its per-row updates into one transaction, matching `ReplaceAssetUsage`'s existing pattern, rather than
N unbatched round-trips with no rollback on a partial failure.

A fourth, lower-severity finding was filed rather than fixed here: loom #59, a transient unreadable
memory store (B7c's own deliberate degrade-not-block tolerance) can now cause a one-pass "flicker"
where withdrawal wrongly retracts real proposals for that store, before this section's own revival fix
brings them back once the store is readable again. Narrower and self-healing after the fix above, and
a real fix needs `internal/asset` and `internal/ledger`'s withdrawal step to share a concept ("which
stores were actually scanned this pass") neither currently has - a design decision, not a bolt-on.

#### A file that never changed size never gains a new feature's data - BUILT 2026-09-23

Issue #49, found while confirming #38 - a session file untouched since before B7b/B7a's tables
existed had a `runs` row but zero `tool_usage`/`compactions`/`asset_usage` rows, forever, because
`NeedsIngest` only ever asked "has this file's size changed", never "does this row have every derived
table a current loom populates". Confirmed directly against this machine's own real ledger: one file
last modified before B7b shipped had a `runs` row whose `size_bytes` matched exactly, so every
`loom report` since treated it as current and skipped it, silently understating the compaction/
occupancy figures this doc itself records.

New `CurrentFeatureVersion` constant (`run.go`) and a `runs.feature_version` column, stamped by
`InsertRun` on every write. `NeedsIngest` now re-reads a file when its size changed **or** its stored
version is below `CurrentFeatureVersion`, regardless of size - the version-stamp shape the issue asked
for a decision on, over a `--force` flag, because it needs nothing from the user, ever, matching
loom's own bias toward discovering rather than asking. `ALTER TABLE ... ADD COLUMN ... DEFAULT 0`
backfills every existing row with `0`, which is below the current value of `1`, so a ledger that
predates this column gets every one of its runs re-ingested once on the next `loom report` -
self-healing the exact gap #49 found, not just closing it for whatever ships next. Verified against
this machine's own real ledger, backed up first: `feature_version` read `1` on all 57 runs afterward,
and `tool_usage`/`compactions` both grew.

#### The first real trial found the promotion rules starving each other - BUILT 2026-09-23

Issue #65, from the AMC trial: `detectBrokenLinks` ran first in `GenerateMemoryFindings` and, on that
machine's real corpus, found 15 broken links - enough on its own to fill the entire 20-slot
`MaxPendingProposals` cap before `detectFilenameSlugDrift` or `detectUnreachableAssets` stored a single
proposal. Worse than an ordering quirk: the issue showed 6 of those 15 broken links pointed at files
that exist, under a name that only fails on a snake-vs-kebab separator drift - the exact thing
`detectFilenameSlugDrift` would have reported, with the actual fix (16 frontmatter lines), if it had
ever been given a slot. Whichever detector runs first and is loudest was silently deciding which
*true* finding a user gets told about.

Fixed with the cheaper of the two shapes the issue itself proposed: `Store` now interleaves every
proposal by kind, round-robin, before the upsert loop that fills the cap (`interleaveByKind`), so no
kind can exhaust `MaxPendingProposals` before every other kind with real findings gets a fair share of
it. `GenerateMemoryFindings`'s own order also changed - `detectFilenameSlugDrift`/
`detectUnreachableAssets` (root cause) now run before `detectBrokenLinks` (symptom), so the two are
tied on standing but the root-cause kind wins the margin once the cap actually cuts a round short. The
stronger shape the issue described - suppressing a `broken_link` proposal outright when the target's
own drifted filename matches, folding it into that file's `filename_slug_drift` evidence instead of
just filling a different slot for it - is real future work, not done here: it needs `detectBrokenLinks`
and `detectFilenameSlugDrift` to share state neither currently does, and the interleave alone already
fixes the reported bug (nothing generator-side gets silently dropped from view again).

Two smaller findings in the same issue, fixed alongside: `[[MEMORY]]` could never resolve, because
`DiscoverAllMemory` deliberately excludes the index itself from the file set the checks run against -
not a missing file, a file that was never going to be in the set that gets a `Slug` registered. New
`asset.MemoryIndexSlug` special-cases it in `detectBrokenLinks`. The other finding - a link to a real
skill, not a memory, is reported broken with rationale text that says "renamed, moved, or never
existed", which is only sometimes true - deferred rather than fixed here: distinguishing the two needs
cross-project skill discovery, which does not exist (`Discover()` is single-project, keyed to cwd;
`DiscoverAllMemory` is the only cross-project scan this package has, and it does not look at skills).

#### broken_link was flagging a permitted convention as a defect - BUILT 2026-09-23

Issue #67, also from the AMC trial, found while auditing loom's own output against the memory
convention it checks: Claude Code's own instructions say a `[[link]]` to a slug that doesn't exist yet
is fine, "it marks something worth writing later, not an error." `detectBrokenLinks` flagged it anyway,
indistinguishably from real rot, because the two look identical on disk - the evidence for the
distinction is intent, which the filesystem does not carry. Measured by hand across this machine's 16
real memory stores: 505 links resolved, 291 within their own store, 29 in a different store, **185
(86% of the 214 `broken_link` findings) pointed at a slug that exists nowhere on the machine at all**.

Fixed with the smaller of the two shapes the issue proposed - drop the "exists nowhere" class entirely,
treat the convention as authoritative - rather than the aggregate-per-store alternative: `detectBrokenLinks`
now also builds a global slug index across every store (`existsAnywhere`), not just each file's own
store, and only reports a link whose target exists somewhere but not here. A target that exists nowhere
at all is silently permitted; a target that exists in the wrong store is still a real, actionable
finding - the reference is real, just scoped wrong. This directly changes #65's own arithmetic, not by
coincidence: with the forward-reference class gone, `broken_link` shrank from 214 findings to roughly
29 on the corpus that motivated it, which on its own no longer fills the pending cap - #65's
interleave-by-kind fix stays, as defense against whichever kind is ever loud again, but this is the fix
that actually explains why it was loud this time.

#### loom propose gained --lane - BUILT 2026-09-23

Issue #68, also from the AMC trial: `loom report`/`loom context` both accept `--lane <lane>` to narrow
to one project directory, `loom propose` did not, and `GenerateMemoryFindings` scans every store
unconditionally with no lane attached to the result. On the machine that found this, 14 of 20 pending
proposals belonged to a different lane than the session running the command - not just noise, a
boundary the tool was crossing uninvited on a machine that deliberately keeps its lanes separate, and
sharp because #65's global cap meant a lane's own findings could be crowded out entirely with no way to
narrow back to them.

`--lane` now filters `loom propose`'s **display** only, to the four B7c memory-finding kinds
(`MemoryFile.Store` is already exactly the same slug `--lane` filters on elsewhere, nothing new needed
discovering). `pin_model`/`revert_policy`/`retire_asset` show regardless of `--lane` and the output says
so: their evidence is not store-shaped, and the policy they write is machine-global, so filtering them
by lane would misrepresent what applying one actually does - the smallest useful version the issue
itself named, rather than pretending otherwise.

Deliberately a display filter, not a generation filter: `Store` still receives the full, unfiltered
generated set on every `--lane` run, never a lane-narrowed one. `Store` withdraws any pending proposal
its input doesn't contain (issue #40) - passing it a filtered subset would have made `--lane` an
accidental way to withdraw every other lane's still-valid proposals as a side effect of narrowing one
session's own view. Verified for real: ran `--lane` against this machine's own dotfiles lane, confirmed
the pending count in the ledger stayed at 20 (nothing withdrawn), only the printed list narrowed.

#### MCP surface widened back - BUILT 2026-09-23

Issue #62, the last of the AMC trial's four findings and the one that revisits an earlier decision
directly. "MCP server shape, narrowed further" (above) cut `query_ledger`, `list_proposals` and
`dismiss_proposal` to CLI-only on reasoning it labelled explicitly as unmeasured - a standing
system-prompt cost, weighed against a substitute a session could always reach via its own shell tool -
and said plainly it would reverse "if a later trial says otherwise." The first real trial did: a
session connected to a real, 205-run ledger could see nothing about cost, occupancy, or proposals -
only `get_recommendation`'s keyword match reached it, and everything the ledger actually knows was
reachable only from a CLI that session had no reason to invoke unprompted. The concern the original
B7 handover raised before any of B7 was built - loom's tools carrying real schema weight against a
skill's - was answered then by narrowing; this trial answered the opposite question, what narrowing
itself costs once the one tool left can't say anything about cost, and settled it the other way.

Restored: `get_cost_summary` and `get_context_occupancy` (the two halves the old combined
`query_ledger` reported, now split, matching what the narrowing decision itself proposed as the
priority order if this ever reversed), and `list_proposals`. All three read-only in the sense that
matters here - none of them ever applies or dismisses anything, `TouchesUserFiles` still gates every
proposal exactly as before. `dismiss_proposal` and `record_outcome` stay cut: both write, and that
half of the original reasoning was never the unmeasured half - it rests on B5's own settled rule that
a decision changing state belongs at the terminal, restated in "MCP server shape, narrowed further"
above, and this trial gave no reason to revisit it. `NewServer` regains its `home` parameter,
constructor-injected as before (never resolved per-request - the exact hermeticity bug fixed once
already), and `home` is why `list_proposals` alone among the three needs it: `GenerateMemoryFindings`
is the one piece of proposal generation that touches the filesystem.

Verified for real over the wire, not just by the in-memory-transport test suite: called `loom serve`
directly and confirmed `tools/list` returns all four tools.

#### advise ignored its own evidence, and escalated on ambiguity - BUILT 2026-09-23

Issues #63 and #64, both from the AMC trial, both about the same command: `loom advise` was giving
worse advice than the ledger's own evidence supported, in two independent ways.

**#63: the evidence existed and nothing consulted it.** `loom policy` could show four agent types with
evidence-backed rows on a 205-run ledger, every one of them landing on `sonnet`, while `loom advise` on
the same ledger still printed "no history yet to personalize from" - the self-tuning loop's own output
was invisible to the one command a live session actually calls. New optional `--agent-type <type>` on
`loom advise` and `agent_type` on `get_recommendation`: when given, `selector.Recommend` takes a third
parameter, `*ledger.PolicyRow`, resolved by the caller (never by `selector` itself, which stays
database-free by the same design as the `[]ledger.AssetRow` it already takes) and, when non-nil, drives
the answer directly - real, measured evidence over a keyword guess, whether the policy was measured or
hand-set, since a decision already made for an agent type is not second-guessed here any more than
`revertRegressedPolicies` second-guesses one with a number. Empty `--agent-type`, or an agent type with
no policy yet, falls through to the unchanged cold-start heuristic.

The same issue's second half: the keyword heuristic escalated to `opus` on a single incidental word.
The example that found it - "investigate why the iOS Device Farm @full leg fails and post the evidence
on the Jira ticket", a debugging task - matched "why" (a planning keyword) and got `opus`/`high`. On
real measured data on the machine that found this, `opus` costs roughly 272x `sonnet`'s per-run cost,
and the two ways to misjudge are not symmetric: guessing too cheap costs a retry, guessing too
expensive costs the difference outright, whether or not it turns out to have been warranted. Fixed two
ways: `"why"` removed from `planningWords` (too common in ordinary debugging phrasing to be a reliable
planning signal on its own), and a new `minPlanningHits = 2` - escalating to the expensive tier now
needs at least two distinct planning-keyword hits, not just a one-word margin over retrieval. Ambiguity
now defaults to the cheap direction, stated in the rationale text itself, not just the code.

**#64: retrieval returned nothing on a lexical miss, including for the exact right answer.** The same
query above scored zero against `devicefarm-public-devices-fail-device-gate` and three other real
memory files with "devicefarm" in the name - a genuine, specific match, missed only because "Device
Farm" (the query's own spacing) and "devicefarm" (the asset's own naming, no separator) are different
token strings under plain word-splitting. New `tokenizeWithCompounds`: every pair of adjacent words,
concatenated with no separator, added as an extra token - "device" + "farm" → "devicefarm" - used for
matching only, on both the query and candidate sides, never for `score`'s denominator (which stays
unigram-only), so a compound match can only add overlap, never inflate the query length it's divided
by and erase its own benefit. `Recommend` also never returns empty when there was something, even weak,
to show: every candidate scoring above zero is tracked, and if nothing clears `minScore`, the top
scorers are returned anyway with a new `BelowThreshold` flag, surfaced in the CLI, MCP output, and a
live-tool test - a caller can discard a weak guess, but cannot discover an asset it was never told
about.

**A real bug found by `/code-review high` before merge, confirmed by reproduction, not just argued.**
`SkillMatch.Score`'s own doc comment promises a 0 to 1 range, and the compound-match fix above broke
it: `queryMatchTokens` (the numerator side) can have more members than `queryTokens` (the denominator)
- every compound is an extra token on top of the unigrams already counted - so a candidate whose own
description independently repeats the same phrase the query uses matches both the unigrams and their
compound separately, and overlap can exceed the query's own token count. Reproduced directly: `"device
farm"` against a candidate whose own description also contains "Device Farm" scored `1.5`. A caller
treating that as a confidence fraction - the CLI's `[%.2f]`, the MCP JSON `score` field - would see a
nonsensical result. Fixed by clamping `score`'s return value to `1`, the minimal fix that restores the
documented invariant without complicating the matching semantics further. The same pass found the
compound tokenizer lowercasing and regex-tokenizing its input twice (once inside its own call to
`tokenize`, once again to build compounds) for no reason - not a correctness bug, but real waste on
every candidate scored on every `Recommend` call.

**A second review pass, on the fix above, found three more.** The efficiency fix just described only
fixed the double-scan for candidates; the query itself (`desc.Text`) was still scanned twice, once
building the plain unigram set, once building the compound set - the exact pattern the previous fix's
own commit message said it had fixed. Restructured into `tokenizeWords` (one lowercase+regex pass,
shared), `unigramSet` and `compoundSet` (both built from that one word list), so `Recommend` now scans
the query once. Second: the compound tokenizer was still called on `a.Name+" "+a.Description` joined
into one string, so the last word of `Name` and the first word of `Description` could form a compound
neither field actually contains - confirmed by reproduction, an asset named "mobile testing device"
with a description starting "farm health checks..." (two unrelated fields) spuriously matched a
"Device Farm" query at `0.6` instead of the true `0.4` from the real, separate "device"/"farm"
overlap. New `compoundsAcrossFields` builds each field's compounds independently and only merges the
resulting sets, never treating a field boundary as two adjacent words in one phrase. Third: `score`'s
clamp fixed the documented range but not determinism among ties it creates - several candidates
clamped to the same `1.0` sorted in whatever order `sort.Slice`'s unstable algorithm happened to land
them, which is not guaranteed consistent across runs on identical input. Switched to `sort.SliceStable`
and the loop that splits `matches` out of the now-sorted `all` changed from a second full scan into a
second slice (an unnecessary second allocation, since a descending-sorted list's qualifying prefix is
always contiguous) to finding that cutoff directly.

The same second pass also found `coldStartModel`'s tie case - both `planningHits` and `retrievalHits`
nonzero and equal - fell into the retrieval-dominant branch and returned its rationale text
unchanged, which is false for a tie: a text matching three planning words and three retrieval words
got told it "matches retrieval/mechanical keywords" with no mention that it matched exactly as many
planning ones. The model/effort answer (`haiku`/`low`) was already the intended one - a genuine tie
is conflicting evidence, not no evidence, and defaults cheap the same reasoning as everywhere else in
this section - only the rationale was dishonest about why. Split into its own case with its own
accurate wording.

Every issue in this section verified for real, not just by unit test: `loom advise --agent-type
Explore` against a scratch ledger with a hand-set policy printed that policy's model, not a cold-start
guess; the `devicefarm` compound match, the weak-signal-does-not-escalate fix, the score-clamp fix,
and the tie-rationale fix all confirmed against real or reproduced query text.

#### A raw driver error was the only signal a resident MCP process had drifted - BUILT 2026-09-24

Issue #74, from a candor-rooted session on 2026-09-24 that hit `list_proposals` and
`get_recommendation` failing identically: `SQL logic error: no such table: artifacts (1)`, while
`query_ledger` against the same ledger, same session, worked fine.

The handover that carried this finding guessed a missing migration. That was wrong. Confirmed
instead by `ps` and `lsof` against every `loom serve` process resident on the machine: each one
held an old binary inode open, from before the artifact-to-asset rename, while `~/go/bin/loom` on
disk had since moved to a newer inode via ordinary rebuilds elsewhere. The plugin's `loom-mcp`
wrapper script (see "MCP server shape, narrowed further") resolves the `loom` binary once, at
server spawn, then `exec`s it for the life of the process - so a session whose server started
before a rebuild keeps running the pre-rename code indefinitely, querying a table
`migrateAssetRename` had already dropped from the shared ledger. `query_ledger` never touches
that table, which is exactly why it kept working and the drift stayed invisible for as long as
the process lived.

Current source was never wrong. Nothing about the failure said so, though: a caller got a raw
SQLite string with no hint that the resident server, not the ledger, was the thing out of date.
A startup schema check would not have caught this either - the process was correct when it
started and drifted only afterward, under a binary rebuild it had no way to observe. The fix that
actually reaches the failure: `explainIfStaleProcess` in `internal/mcp/server.go` recognizes a
`no such table` error at the point every handler returns one and wraps it with what is actually
wrong and what to do about it ("restart this session so the server relaunches against the
current binary"), rather than leaving the caller to diagnose a raw driver string. Applied
uniformly across all four handlers that touch the ledger, not just the two issue #74 named,
since the same staleness can in principle affect any table a future rename or schema change
touches - narrowing the fix to only the two tools that happened to fail this time would leave the
other two equally blind the next time it's a different table.

Verified end to end, not just by constructing the wrapped error directly: a test opens a second
connection to the same SQLite file a live test server is already using, drops the `assets` table
out from under it, and confirms the real MCP call path - not just the helper function in
isolation - returns the actionable message.

**Two real findings from `/code-review high` on the first version of this fix.** First: the match
covered only `"no such table"`, missing `"no such column"` - a renamed or dropped column produces
the identical stale-process symptom as a renamed or dropped table, and the doc comment's own claim
to cover "any table a future rename or schema change touches" was false for that case. Widened to
match both. Second, more substantive: a bare substring match on the driver message has no way to
tell a genuinely stale process apart from an unrelated bug in freshly written code that references
a table or column that never existed - the original wording asserted staleness as fact regardless.
Reworded to state it as a likely, checkable cause ("this usually means...") rather than a
diagnosis, with an explicit "if restarting doesn't fix it, this is a different, real bug" - the
same principle as constraint 11, never presenting a guess with the confidence of a measured
finding, applied to the error message itself, not just to loom's own proposals.

#### A store unreadable for one pass could wrongly withdraw its own real proposals - BUILT 2026-09-24

Issue #59, found by code review while shipping #40's withdrawal mechanism (`WithdrawStalePending`,
"a pending proposal is never withdrawn when its evidence stops holding"). `DiscoverAllMemory`
silently skips any store whose `memory/` directory fails to read - a deliberate B7c decision,
constraint 7, one project's permission problem must not take every other store's findings down
with it. Before #40 that was harmless: a skipped store just meant one pass with no findings for
it. After #40, a skipped store looked, from `WithdrawStalePending`'s point of view, identical to a
store whose findings genuinely stopped being true - every real, unchanged `broken_link`/
`unreachable_asset`/`filename_slug_drift`/`promote_memory_duplicate` proposal for that store got
marked withdrawn on the one pass it couldn't be read, even though nothing about the underlying
facts changed. Self-healing on the next successful pass (#40's own revival rule brings it back
once the scan reproduces the identical finding), but a real, if narrow, flicker in between.

Fixing it meant crossing a package boundary that did not have the vocabulary for it:
`ledger.WithdrawStalePending` has no concept of "store", `internal/asset` has no concept of a
proposal. Resolved without teaching either package about the other's concept. `DiscoverAllMemory`
now returns a second value alongside its files, a new `MemoryScanCoverage` struct (`Present`, every
store directory found this pass; `Scanned`, the subset whose `memory/` was actually readable).
`GenerateMemoryFindings` forwards it untouched - it has nothing to add. `propose.Store` is the one
place that already understood both sides (it holds a `*ledger.DB` and it already builds proposal
evidence), so it does the store-awareness entirely on its own: new `LaneScopedKinds` (promoted from
a var of the same name and shape already living in `cmd/loom/propose.go` for issue #68's `--lane`
filter) and `EvidenceStores` (also generalizing that file's inline evidence-parsing struct, now
shared by both the lane filter and this fix, removing a duplicate) identify which pending proposals
are store-scoped and which store(s) each one's evidence names. Before withdrawing, any pending
lane-scoped proposal the current pass did not reproduce goes through `needsProtection`, which
decides whether to treat it as reproduced anyway. `ledger.WithdrawStalePending` itself is untouched
- the crossing happens entirely on `propose`'s side, which is where both concepts it needs were
already in scope.

**A second `/code-review high` round on the first version found two more real gaps, both in
`needsProtection`'s design, not implementation bugs in what it did do.** First: the first version
only handled a single unreadable store, but treated the *root* `<home>/.claude/projects` itself
going missing or unreadable for one pass as "confirmed empty, nothing to protect" - which would
mass-withdraw every real, unchanged lane-scoped proposal across every store, reproducing issue
#59's own failure mode one directory level up (a wrong `$HOME` for one invocation, a mount hiccup).
Second, the opposite problem: with no way to tell "transiently unreadable" apart from "gone for
good", a store deleted permanently would have its stale proposals protected forever instead of
ever withdrawing - stuck pending, defeating #40's whole purpose in a new way. Both fixed together
in `needsProtection`, using a distinction `MemoryScanCoverage` already carried but the first version
didn't use: `Present == nil` (the root itself was never successfully enumerated this pass) protects
every lane-scoped proposal, regardless of which store it names - nothing this pass found can be
trusted as evidence of absence. Otherwise, a store present in `Present` but missing from `Scanned`
is the real transient-failure case (protect); a store missing from `Present` entirely no longer
exists as a project at all, which is a legitimate reason its own findings are gone too, not a scan
failure (let it withdraw normally, self-healing exactly as #40 intended).

Verified for real at every stage, not just by unit test: built the binary against a scratch `HOME`
three separate times. First round: raised a genuine `unreachable_asset` finding, made the store's
`memory/` directory unreadable (a file where a directory should be) and reran `loom propose` - the
proposal stayed pending; fixed the store for real (added the missing `MEMORY.md` entry) and reran
again - the proposal withdrew. Second round, after the review findings: deleted the whole store's
project directory (not just `memory/`) - the proposal withdrew, confirming a genuinely gone store
still self-heals. Third round: pointed `$HOME` at a directory with no `.claude/projects` at all,
against the same ledger that had a real pending proposal from a previous run - the proposal stayed
pending, confirming a root-level scan failure protects rather than mass-withdrawing.

**A third `/code-review high` round on the second fix found nothing new that first round's own
diagnosis hadn't already covered** - it independently re-derived the root-scan-failure gap while
mid-review, then confirmed the shipped fix already closed it by reading the current code rather
than trusting the commit message. Two lower-severity observations kept: `LaneScopedKinds`' comment
now says explicitly that a future per-store finding kind must be added there too, since nothing
else enforces it. The other, that `needsProtection`'s protection has no expiry - a persistently
wrong `$HOME`, not just a one-pass blip, protects a proposal forever instead of ever letting it
withdraw - is filed as [#76](https://github.com/azva-co/loom/issues/76), deliberately deferred:
the failure direction is the safe one (stuck pending, bounded by the cap, not wrongly discarding a
real finding), and a real fix needs new ledger state (a staleness bound), not a line here.

**A fourth review round found one more real, live bug, in a display path rather than the
withdrawal logic every earlier round focused on.** `list_proposals`' Summary/Rationale text was
built from a map keyed only by this pass's freshly generated proposals - correct before #59, since
a pending row could never exist outside that set by construction (anything not reproduced was
withdrawn). `needsProtection` broke that invariant on purpose: a protected row is pending precisely
*because* it wasn't reproduced this pass, so the lookup missed and the row came back with a blank
Summary and Rationale over MCP - during the exact failure window this whole fix exists to handle
gracefully, just in the field the caller actually reads rather than the withdrawal decision itself.
`cmd/loom`'s own CLI listing was unaffected (it already rebuilds its one-line summary from each
row's own stored evidence, not from a generated-only map). Fixed by extracting that same logic -
moved, not rewritten - into `propose.SummaryFor`, shared by both the CLI and the MCP handler, which
now falls back to it (plus an honest, generic Rationale explicitly saying the row was not rescanned
this pass) whenever the generated-set lookup misses. Three more findings from the same round -
`needsProtection` conflating malformed evidence with a real coverage gap, `Store`'s protection path
being tied to the concrete `asset.MemoryScanCoverage` type rather than an abstraction, and
`EvidenceStores` short-circuiting on `Store` before checking `Stores` if evidence somehow carried
both - are each real only for a finding kind or evidence shape that does not exist yet on any
current caller; left as documentation (two doc-comment clarifications, on `Store` and
`MemoryScanCoverage`) rather than new runtime code, consistent with not designing for a requirement
nothing has yet.

#### A link to a real skill read as silence, not a defect - BUILT 2026-09-24

Issue #66, split out from #65 originally. `detectBrokenLinks` flags every `[[link]]` that doesn't
resolve to a memory file's frontmatter name in the same store - but on a real corpus, `entity-team`,
`entity-docs` and `sprint-management` all resolved that way while existing as real skills under
`~/.claude/skills/`. Written before issue #67 shipped, though: re-verified against the current code
before building anything, and #67's own fix (a target that exists nowhere at all is a permitted
forward reference, not a defect) already stopped these three from being wrongly flagged as broken -
confirmed by reproducing the exact shape in a scratch fixture and checking the real output, not by
assuming the issue's original description still matched current behavior. What #67 left behind
instead: total silence. A link to a real skill and a link to a note nobody has written yet now look
identical - both permitted forward references - even though only one of them will ever resolve, since
a `[[link]]` only ever resolves against a memory's own frontmatter name, never a skill's.

New `asset.DiscoverGlobalSkills(home)`, a thin wrapper around the existing `scanSkillDir` - the one
skill location that actually is cross-project-safe to enumerate without a specific cwd. The other
`SkillDirs` entry, a project's own `.claude/skills`, has no such equivalent: a project's working
directory is not reliably recoverable from its `~/.claude/projects/<slug>` state-storage path, since
the slug's hyphen substitution is lossy - real, buildable scope stops at the global directory, not
the wider "cross-project skill discovery" the issue's own "shape of a fix" speculated about before
the code existed to check it against.

`detectBrokenLinks` gained a third outcome, not just two: resolves locally (fine), exists in a
different store (issue #67's original real finding, checked first since it is the more actionable
one), exists nowhere as a memory but is a known skill (issue #66 - now flagged, with rationale that
correctly says what the target actually is), or exists nowhere at all (issue #67's forward
reference, still silent). Evidence carries a new `target_is_skill` field, not just an in-process
string choice - found by code review before this shipped: `SummaryFor` rebuilds display text from
stored evidence alone, with no access to which branch of `detectBrokenLinks` generated it, so
without a stored field the CLI's own re-display of an already-flagged skill-shadow row would fall
back to the wrong ("exists but not in this store") wording - reproduced directly against the real
binary before the fix, confirmed correct after.

**`/code-review high` on the first version found one severe finding and three real, smaller ones.**
The severe one: `target_is_skill` was written into every `KindBrokenLink` evidence map
unconditionally, true or false - which changed `Proposal.Hash()` for every plain cross-store finding
too, not just skill-shadow ones, and `UpsertProposal` resets a row's status to pending on any hash
change regardless of its prior status. On the first pass after that version shipped, every previously
dismissed or applied cross-store `broken_link` proposal on any real ledger would have silently
reverted to pending - the same regression class #77 fixed, reintroduced by this PR's own new field.
Fixed by only setting the key when true, never a literal `false`; confirmed against the real binary,
not just a unit test: dismissed a genuine cross-store finding, let an entirely unrelated skill appear
on disk, reran `loom propose` - the dismissal held.

Two more, both in how `knownSkills` gets built. First: keyed only by `assetFromFile`'s resolved
`Name` (frontmatter preferred, else the directory name) - but a `[[link]]` author references what
they actually invoke the skill as, the directory name, which can drift from its own frontmatter (a
real, documented failure mode on this exact codebase's history - `~/.claude/CLAUDE.md` itself notes
six skills sitting with broken frontmatter for months unnoticed). Fixed by registering both names.
Second: `DiscoverGlobalSkills` returns `KindReference` assets too (a flat `.md` file, never actually
loadable as a skill), and the first version labelled a link to one "a skill" anyway - factually
wrong. Fixed by filtering to `KindSkill` only.

One deferred rather than fixed here at the time: the identical silence gap for agents and plans,
which have the same global-plus-per-project shape as skills in `asset.DefaultLocations` - filed as
[#78](https://github.com/azva-co/loom/issues/78), since no real corpus evidence existed yet for
that shape the way #66 itself had for skills, and building it speculatively would be exactly the
kind of guess constraint 11 warns against. The same round also noted the three-way classification
logic was duplicated in structurally different shapes between `detectBrokenLinks` and `SummaryFor`,
and that the skills-directory scan ran unconditionally on every `GenerateMemoryFindings` call even
when nothing needed it - the first left as-is at the time (matches an existing pattern this codebase
already accepts elsewhere, `DiscoverAllMemory` itself scans unconditionally on every call), the
second proportionate to revisit only once #78 added a second kind worth unifying against.

**Update, same day: #78 was picked up anyway, on explicit instruction overriding the deferral
above** - see "The skill-shadow fix generalized to agents and plans" below for what shipped,
including the classification-logic unification this paragraph left for later.

#### A root scan failure protected forever, not just for a blip - BUILT 2026-09-24

Issue #76, filed from #59's third review round. `needsProtection` treats a root-scan failure
(`<home>/.claude/projects` itself could not be enumerated this pass) as a reason to protect every
lane-scoped pending proposal, correct for a one-pass blip. But nothing distinguished that from a
persistently wrong or misconfigured `$HOME` - a cron job or systemd unit with a stripped
environment, say - where the root never resolves correctly across many passes and every lane-scoped
proposal stays protected forever, occupying a slot against `MaxPendingProposals` long after the
underlying finding may have stopped being true. Deliberately lower severity than #59 itself: the
failure direction is the safe one (stuck pending, not silently discarding a real finding), which is
why this was filed and deferred rather than folded into #59's own already-large PR.

New ledger state, the smallest shape that answers the actual question: not per-proposal (the
condition that triggers this - the root itself unscannable - is inherently global, not about any one
proposal), a singleton `memory_root_scan` table holding one nullable timestamp, `unscanned_since`.
`ledger.RecordRootScanCoverage(scanned, now)` clears it the moment a pass succeeds (whatever
happened before does not matter once the root works again) and, on a failure, sets it only the first
time - a second, third, nth consecutive failure leaves the original timestamp alone, so `Store` can
tell how long the streak has actually run, not just that it is currently failing. New
`RootScanFailureTolerance = 7 * 24 * time.Hour` (a week - long enough to rule out a closed laptop
lid over a weekend, short enough that a genuinely broken environment does not sit silently for
months): once exceeded, `needsProtection` stops honoring the root-failure case and falls back to
normal withdrawal, the same way a genuinely deleted store already does.

`Store`'s `coverage` parameter became `*asset.MemoryScanCoverage` (a pointer, not a value) to close
a real ambiguity this surfaced: the zero-value `MemoryScanCoverage{}` was already the sentinel for
"the caller never even attempted a memory scan" (existing DB-only tests), but it is also exactly
what a genuine root-scan failure produces - indistinguishable at the value level. Recording a
"failure" every time a DB-only caller passed the old sentinel would have started a fake streak in
the ledger for a scan that was never attempted. `nil` now means "no scan attempted, nothing to
record or protect"; a non-nil pointer, even to a zero-value struct, means "a real scan happened,
here is what it found" - unambiguous, and `Store` only calls `RecordRootScanCoverage` when it holds
the latter.

Verified against the real binary for the parts a real clock can exercise (the streak gets recorded
and protection holds through the first failing pass), and against the ledger and `Store` directly
for the timing that would take a week of real time to observe: a streak's tolerance window measured
from its own start survives a reset; a proposal at 6 days 23 hours into a failing streak stays
protected; at a week and one hour, it is withdrawn.

**`/code-review high` on the first version found two real gaps.** First: `Store` only recorded root-
scan coverage when something happened to be pending, gated as a query-avoidance optimization - but
that meant a successful scan with nothing pending never cleared an in-progress streak either. A
later, unrelated proposal appearing under a fresh failure would silently inherit the stale,
already-expired streak start from long before and lose its own one-pass grace period immediately -
the exact bug this whole fix exists to prevent, reproduced by the reviewer directly. Fixed by
recording coverage whenever a real scan happened, regardless of what is currently pending; the
per-proposal loop underneath still costs nothing extra when nothing is pending; only the recording
itself was ever wrongly gated. Second: `RecordRootScanCoverage`'s failure path did a `SELECT` then a
separate write - a genuine race between two `Store` calls close together (the CLI and the MCP server
against the same ledger, or two overlapping MCP calls) could each see no existing streak and each
write their own timestamp, with whichever finished last silently overwriting the true first-failure
time. Fixed with one atomic `INSERT ... ON CONFLICT DO UPDATE ... RETURNING` statement, `COALESCE`
keeping the existing value across every consecutive failure and only taking the new one the first
time - no separate read, nothing to race.

**A third round found the second fix's own doc comment had gone stale**, still describing the
pre-refactor value-type sentinel ("pass the zero value") after `coverage` became a pointer mid-fix -
a future maintainer following it literally would pass `&asset.MemoryScanCoverage{}`, which is a real
scan reporting nothing found, not "no scan attempted," and would start a genuine failure streak for
a call that never scanned. Corrected to state the actual sentinel (`nil`). One more, low severity,
left as-is: `RecordRootScanCoverage`'s success path writes unconditionally on every call rather than
checking first whether the streak is already clear - a guard would need its own read first, which
would cost more on the common case (still working fine) than the occasional unnecessary write it
would save.

#### The skill-shadow fix generalized to agents and plans - BUILT 2026-09-24

Issue #78, filed from #66's own review round and picked up on explicit instruction despite the
issue's own deferral reasoning (no measured corpus case, unlike #66's real `entity-team`). `[[link]]`
naming a real agent or plan reproduces the identical silence #66 fixed for skills - a `[[link]]`
never resolves against an agent's or a plan's name either, only a memory's frontmatter name, and
`asset.DefaultLocations` gives `AgentDirs`/`PlanDirs` the same global-plus-per-project shape
`SkillDirs` has. New `asset.DiscoverGlobalAgents`/`DiscoverGlobalPlans`, thin wrappers mirroring
`DiscoverGlobalSkills` exactly, scoped the same way (global directory only - a project's own
working directory is not recoverable from its `~/.claude/projects/<slug>` state-storage path).

Took the review's own second suggestion seriously this time rather than deferring it again:
`detectBrokenLinks` and `SummaryFor` had the three-way classification logic duplicated in
structurally different shapes since #66 shipped, flagged as worth unifying "before adding a fourth
branch" - now a fourth and fifth branch both needed adding at once, so this was the moment. New
`brokenLinkText(filename, targetSlug, isMemoryIndex, otherKind)` is the one function that knows how
to render every classification; both `detectBrokenLinks` (fresh scan data) and `SummaryFor` (stored
evidence only) call it, so the two can no longer drift into different wording for the same case.

One thing #78's own "shape of a fix" suggested but turned out to be the wrong call once weighed
against #66's own review lesson: a single shared `target_kind` evidence field, replacing
`target_is_skill`. That would rename a field #66 already shipped - exactly the hash-changing mistake
#66's first review round caught and fixed, reapplied to itself. `target_is_skill` stays untouched;
`target_is_agent`/`target_is_plan` are new, parallel boolean keys, each set only when true, never as
a literal `false` (same reasoning as before: an unconditional key on the plain cross-store case would
change every existing cross-store finding's hash too). `SummaryFor` reconstructs the single
`otherKind` `brokenLinkText` actually needs by checking whichever of the three booleans is present,
so a row stored before this shipped - carrying only `target_is_skill`, the one key that existed then
- still resolves correctly.

`linkTargetNames` registers both a discovered asset's frontmatter name and its on-disk name,
generalizing #66's skill-only drift handling. Unlike skills, an agent or a plan can be either of
`scanMarkdownDir`'s two shapes (a flat "name.md" or a "name/SKILL.md" subdirectory) under the
identical `Kind`, where `scanSkillDir` splits the two shapes into different Kinds instead - so the
on-disk name can't be read off `Kind` the way it could for skills alone.

**`/code-review high` found five findings, four real and fixed.** `knownOtherAssets` collapsing a
name that exists as more than one kind to "whichever registers last" was an accident of call order,
not a real answer - changed to first-registration-wins, with the precedence now the fixed, documented
order the calls are written in (skill, agent, plan). The stale "deferred" paragraph directly above
this one, from #66's own write-up, still asserted #78 as open after this same diff closed it -
corrected with a pointer rather than silently rewritten, since the reasoning it recorded was real at
the time. `linkTargetNames`'s first version re-derived the on-disk name by sniffing `Path`'s basename
for a literal `"SKILL.md"`, duplicating a computation `assetFromFile`'s own caller (`scanSkillDir`/
`scanMarkdownDir`) had already done once and discarded - fixed by carrying it forward instead, a new
`asset.Asset.OnDiskName` field populated where it was already known, so `linkTargetNames` reads it
rather than re-deriving it and cannot silently go stale if the directory-shape convention ever
changes. `brokenLinkKindNoun`'s lookup had no fallback for a future kind missing an entry - would
have silently rendered "which is , not a memory" - given a documented pairing requirement plus a
fallback to the raw kind string, so a forgotten entry reads as a defect worth reporting rather than a
malformed sentence nobody would notice. The fifth, three sequential directory scans (skill, agent,
plan) where one existed before, on the MCP hot path `list_proposals` calls every time - left as-is,
matching this same PR's own precedent for the equivalent single-scan cost in #66, and this codebase's
established acceptance of `DiscoverAllMemory` scanning unconditionally on every call already.

Verified against the real binary: a memory file linking to a real agent and a real plan under
`~/.claude/agents`/`~/.claude/plans`, alongside a genuinely unwritten forward reference - both the
agent and plan links flagged with kind-specific wording, the forward reference stayed silent.

**A third round found no correctness bugs** - the two earlier rounds had already caught what a
fresh pass would normally find first, confirmed independently re-derived and then verified already
fixed. New `TestBrokenLinkKindsStayInSync`, from the round's one worthwhile suggestion: the "other
asset" kind list is spelled out independently in four places (`registerOtherAssets`' calls,
`detectBrokenLinks`' evidence-writing switch, `SummaryFor`'s evidence-reading switch,
`brokenLinkKindNoun`), with nothing but a comment holding them together. The test walks one
canonical kind list through both the write path and the read path and checks they agree, so a kind
added to one of the four places but not the others fails a test rather than silently degrading at
runtime the way `brokenLinkKindNoun`'s own fallback already tolerates. Two smaller suggestions from
the same round (extracting a shared helper for `DiscoverGlobalAgents`/`DiscoverGlobalPlans`'s
near-identical bodies; a filter check in `registerOtherAssets` that is dead code for two of its
three callers) were weighed and left as-is - each is two one-line functions or one harmless,
already-explained guard, not real risk.

#### CI cost hygiene - BUILT 2026-09-24

A cross-session handover, relayed while a payment failure had dropped the account to GitHub's free
Actions allowance and blocked another repo's workflow entirely: loom's own CI was running fine at
the time (confirmed by checking, not assumed from the handover's premise), but the underlying
practice is worth having regardless of whether loom is the repo actually blocked.

`ci.yml` had five separate ubuntu jobs (`test`, `lint`, `govulncheck`, `plugin`, `secrets`). Actions
bills every job at least a full minute regardless of how little it runs, so five jobs was a
five-minute floor before any of them did real work. Merged `test`/`lint`/`govulncheck`/`plugin` into
one `ci` job - same checks, one runner, one checkout, one Go setup. `secrets` stays split out
deliberately: it needs `pull-requests: write` (gitleaks posts a PR comment on a find) and a
full-history checkout, and widening every other step's permissions just to save one more job would
trade least privilege for a small saving not worth it. No macOS/Windows legs exist in this workflow,
so the handover's third suggestion (gate expensive OS legs to push-only) did not apply here.

Added a `concurrency` group so a new push to a PR cancels whatever run was still going for that same
ref - never for `main`, where every push is a merge that should run to completion and be
individually visible. A superseded run left going to completion is pure waste under per-minute
billing, not just slower feedback.

Also added `persist-credentials: false` on every checkout (the handover's artipacked note): by
default `actions/checkout` leaves the job's token in the local git config after checkout, readable or
exfiltratable by any later step or a compromised dependency in one; nothing in this workflow pushes,
so there is nothing that needs it left in place.

New `scripts/ci.sh` runs the same gate locally in one command, for the window Actions can't run at
all and for ordinary pre-PR use - `CONTRIBUTING.md` and `CLAUDE.md` both point at it now.

**The trade this merge actually makes, stated plainly rather than left implicit:** billed job-minutes
for wall-clock time, and for the GitHub PR checks UI's own granularity. Four independent parallel
jobs finish in roughly the slowest one's duration; one sequential job finishes in roughly the sum of
all of them, and a fast-to-detect problem (an unused import) now waits behind slower steps (the test
suite) that used to report on their own runner at the same time. The checks list a reviewer sees also
went from five independent pass/fail indicators to two - a PR that only fails lint now shows one red
`ci` with no indication which of five steps broke without opening the log, where it used to show
`lint` red and the other four green at a glance (found by code review, before this shipped - not
fixed, since restoring it means restoring the five separate jobs, the opposite of this merge's whole
point, but worth saying plainly rather than discovering by surprise the first time a check fails). A
fifth review round added one more instance of the same trade: GitHub's own "re-run failed jobs"
button used to re-run only the one job that actually failed (roughly a minute); now it re-runs the
whole merged `ci` job every time, which can offset this PR's own per-push savings on any PR needing
more than one retry. Worth paying deliberately, not by accident: fewer billed minutes on a clean run,
slower feedback, coarser failure-attribution, and a costlier retry on one that is not.

A further cost lever the same review round raised and this merge does not take: path-based gating
(skipping the job entirely for a docs-only change, say `CHANGELOG.md` or this file). Not implemented
here - genuinely a different, separate lever from anything the original handover asked about (job
count, concurrency, OS-leg gating), and worth its own deliberate pass rather than folding into an
already-large diff. A candidate for later, not a gap in this one.

**`/code-review high` found six real issues in the first version, all fixed.** Two were confirmed by
reading the actual pinned actions' source, not assumed from documentation: `golang/govulncheck-action`
defaults `repo-checkout` to `true`, which ran its own internal checkout with `persist-credentials`
defaulting to `true` - silently re-persisting the token this job's own checkout had just disabled,
undoing the hardening two steps later. And the action's `go-version-input` defaults to `'stable'`
and always wins over `go-version-file` in `actions/setup-go`'s own resolution order, so
`govulncheck` was silently scanning under whatever Go happened to be "stable" on the runner, not the
version `go.mod` pins - true before this merge too, just newly visible once "one Go setup" became a
claim this diff's own comment made. Fixed with `repo-checkout: false` and an explicit
`go-version-input: ''` (falsy, so resolution falls through to `go-version-file`).

Third: merging four independent jobs into one meant a failure with no override stopped every later
step, unlike the four separate jobs this replaced, which all ran and reported regardless of each
other's outcome - a compile error would have hidden an unrelated lint issue until a second push.
Fixed with `if: ${{ !cancelled() }}` on vet/test/lint/govulncheck (not `always()`, which would also
force them to run through a genuine cancellation from the concurrency group above - exactly the
minutes that group exists to stop spending). Fourth: the unpinned plugin-manifest npm install ran as
the last step of a job that had already accumulated the Go module and build caches, a real if modest
blast-radius increase over its own previous isolated job - moved to run first, right after checkout,
before Go is even set up. Fifth: `scripts/ci.sh`'s exit code didn't distinguish a real check failure
from a tool simply not being installed, the one moment there is no real CI to cross-check against -
now exits `2` specifically for "incomplete, install the missing tool," distinct from `0` and from
whatever a real failure's own tool produces via `set -e`. Sixth: the script claimed its local tool
versions ran unpinned "unlike CI, which pins each one" - true for `golangci-lint` (`version: v2.13.2`
in `ci.yml`), false for `govulncheck`, which both sides have always installed at `@latest` with
nothing to compare against (confirmed by reading `govulncheck-action`'s own source, same as the
earlier findings above). Corrected to say which is actually true for which tool, rather than a
printed-but-uncompared version implying a pin that was never there.

**A second `/code-review high` round found three more real issues, all confirmed against the pinned
action's actual source rather than assumed.** The first round's own fix for `govulncheck-action`
(`repo-checkout: false`, `go-version-input: ''`) stopped the action's internal checkout and forced
its version resolution through correctly, but the action's internal `actions/setup-go` step has no
matching override and runs unconditionally regardless - the job was still paying to resolve Go and
restore its cache a second time, the exact redundant cost the job's own "one Go setup" comment
claimed did not exist. Fixed by dropping the action entirely: `golang/govulncheck-action`'s own steps
past checkout and setup-go are just `go install golang.org/x/vuln/cmd/govulncheck@latest` followed by
`govulncheck ./...`, so this job now runs those two lines directly against the Go it already set up
once at the top - genuinely one setup, not a claim about one.

Second: `Build` was the one step in the merged job without `if: ${{ !cancelled() }}` - an earlier
step's failure (the plugin-manifest check, which now runs first) would skip it the normal way, while
`Vet`/`Test`/lint/govulncheck (already carrying the condition) kept running regardless. The one step
the "let every check still run" rationale was written to cover was the one step it was not applied
to. Fixed by adding the same condition to `setup-go` and `Build` too, so everything from the
plugin-manifest check onward runs independently of what came before it, the checkout step itself
being the only genuine hard gate.

Third: `scripts/ci.sh` claimed to mirror the workflow "step for step", which was true for the list of
checks but not for two structural things - the plugin-manifest check's new position (first in CI, for
a cache-isolation reason that does not apply to a developer's own persistent machine, so it stayed
last locally) and, more substantively, `set -e` meant a local build failure stopped every later check
from running at all, silently losing the exact multi-round-trip guarantee `!cancelled()` had just
been added to CI to provide. Rewritten without `set -e`: every check now runs and reports regardless
of an earlier one's outcome, with a new `EXIT_FAILED=1` distinct from `EXIT_INCOMPLETE=2` so the two
failure classes stay distinguishable through the restructuring. Verified directly, not just read:
built with an intentionally broken `main.go`, confirmed build/vet/test/lint/govulncheck all correctly
reported failed while gitleaks and the plugin-manifest check still ran and passed independently, then
restored the file via git and confirmed a clean run again.

**A third round found two real design flaws in the mechanism the previous two rounds had just built,
plus the version-pinning claim above.** First: `!cancelled()` alone, checked directly against a live
`gh api` call showing branch protection's required checks had not actually updated yet, turned out to
be the smaller of two problems that phrase covers - it is true whenever the job was not cancelled,
which says nothing about whether `checkout` itself actually succeeded. A checkout failing for its own
reason (a transient clone or auth error, not a cancellation) would leave every later step still
attempting to run against a workspace that was never populated, one clean failure becoming up to six
confusing ones - directly contradicting this doc's own earlier claim that checkout was "the only
genuine hard gate." Fixed with an explicit `steps.checkout.outcome == 'success'` alongside
`!cancelled()` on every step from `setup-go` onward, and a much louder comment at the top of that run
of steps: the condition has already been missed twice within this same PR's own history (round one
omitted it entirely; round two added it everywhere except `Build`), so the comment now says exactly
that, addressed to whoever adds a seventh step here next.

Second, a genuine GitHub Actions behavior neither of the first two rounds had reason to know about:
a concurrency group cancels a still-*queued* run the moment a new run joins the same group,
regardless of `cancel-in-progress` - that setting only protects an already-*running* run. Three
pushes to `main` in quick succession (A running, B queued, C arrives) would silently drop B's queued
run entirely, contradicting the concurrency block's own comment that every push to `main` "should run
to completion and be individually visible." Fixed by keying the group on `github.run_id` (unique per
run) for anything that is not a `pull_request`, so every push to `main` gets its own group of one and
can never collide with or cancel another main push's run; only PR runs still share a group keyed by
ref, which is the collision that group is actually meant to create.

**A fourth round found two real gaps and, independently, settled a maintenance concern the third
round had only documented.** No step in the merged job had a `timeout-minutes`: the four separate
jobs it replaced meant a hang in `test` (an unreachable network call blocking forever, say) still
left `lint`/`govulncheck`/`plugin` visible on their own runners; merged into one job, the same hang
now silences everything after it until GitHub's own 360-minute default finally kills the job. Not
fully fixable without un-merging - the point of this diff - but bounded: `timeout-minutes: 15` on
`ci` (generous for a job that normally finishes in well under five), `10` on `secrets`.

Second: the `if: ${{ !cancelled() && steps.checkout.outcome == 'success' }}` condition, hand-copied
onto six steps, is exactly the pattern that had already caused two real regressions earlier in this
same file's history (round one omitted it; round two added it everywhere except `Build`) - the third
round's own fix for `Build` was itself another hand-copy of the same six-way duplication, not a
structural fix for the duplication itself. Replaced with a YAML anchor: `if: &gate ${{ ... }}` once,
`if: *gate` everywhere else, verified to resolve identically on every step by parsing the file and
printing each step's resolved condition. A future step with a missing gate is now a visibly absent
`if: *gate` line, not a subtly wrong hand-typed expression - the actual defect class this pattern kept
producing, closed structurally rather than documented harder a third time.

Third, in `scripts/ci.sh`: the same "four near-identical blocks despite `run()`/`installed()` helpers
existing" observation from the second round, raised again independently by the fourth - two separate
review passes flagging the same duplication is a real signal, not a one-off nitpick. New
`optional_check NAME TOOL HINT VERSION_CMD... -- CHECK_CMD...` collapses each of lint/govulncheck/
gitleaks/plugin-manifests to a single call instead of a ~10-line block, verified against every
scenario already covered: a clean run, a missing required tool, a missing optional tool, and a real
check failure with later checks still running - all four confirmed identical to before the refactor.

**A fifth round found three more real, smaller issues in `scripts/ci.sh`, plus the re-run-granularity
trade above.** A comment claimed the script installs `govulncheck` at `@latest` the way `ci.yml`
does - it never did, only checks whether the tool is already on `PATH` and skips with a hint if not;
corrected. `optional_check`'s version-print expanded `"${version_cmd[@]}"` without checking it was
non-empty first - harmless today (all four calls supply one), but expanding an empty array under
`set -u` throws an unbound-variable error on bash older than 4.4 (macOS's own default `/bin/bash` is
3.2), which would abort the whole script for a future call that omits a version command - exactly the
"one step kills everything after it" failure this file was rewritten to stop doing. Guarded with a
length check first. And `build`/`vet`/`test` were still three hand-copied two-line blocks, the same
shape `optional_check` exists to collapse for the other four checks - new `required_check` (no
`installed()` branching needed, `go` is already checked once before any of these run) for the same
reason.

Two more from the same round weighed and left as-is: running this script via `sh scripts/ci.sh`
(bypassing the shebang, which already declares bash) fails differently and lands on exit 2, which
could look like "incomplete" rather than an obvious crash - out of the documented invocation pattern
(`./scripts/ci.sh`, matching how `CONTRIBUTING.md`/`CLAUDE.md` describe it), not fixed. And the script
hand-duplicates a couple of literal command strings from `ci.yml` (the `govulncheck` install line, the
plugin-validate invocation) with no shared source between YAML and shell - already stated plainly in
the file's own header as an accepted limitation of having two separate execution contexts, not a new
finding needing a different answer.

Branch protection's required status checks were updated to match (`ci`, `secrets`, replacing the
five old names) - confirmed with the owner before changing it, both that the change should happen at
all and that it should happen once the workflow itself was ready, not before.

#### The review-loop rule had a quality stop condition and no cost ceiling - BUILT 2026-09-25

Issue #84. `CLAUDE.md`'s own re-run guidance - "stop re-running once a pass comes back clean, not
before" - names only a quality bar, and the CI cost-hygiene PR just above is real, if moderate,
evidence of the shape: five `/code-review high` rounds, four of them finding a genuine behavioral
bug, before a pass came back without one. A sibling repo (`session-exchange`) ran the identical rule
to its actual conclusion first: eleven `high` passes on one PR, the last several changing only prose
in `CONTRIBUTING.md`, two of them dying on the 600s stall watchdog and returning nothing at all.
Filed here because the rule originated in this file, with the explicit question of whether loom
wants the same fix or a different one, given `scripts/ci.sh` and a stronger automated gate than that
other repo has.

Adopted the same structural fix rather than a bare round-count ceiling, since a numeric cap treats
the symptom (too many rounds) without touching the cause (each round reviewing the same untouched
diff again). Two additions to the existing clean-stop rule, not a replacement for it: scope each
re-run to what the *last fix* touched, not the whole original diff, since reviewing an unchanged
region repeatedly is exactly how a clean-stop-only rule turns into eleven rounds on a 1559-line diff;
and treat a pass whose only findings are wording or comments, not behavior, as the real stop signal -
read the diff yourself at that point and say so in the PR body, rather than spending another round
on prose. A pass with zero findings still stops immediately, unchanged. Not adopted: a hard numeric
ceiling on its own - loom's own five-round experience found real bugs through round four, later than
`session-exchange`'s pattern decayed, so a small fixed cap would have cut off genuine findings here
specifically, the exact case-by-case judgment a bare number can't make.

### B7 scope, agreed 2026-09-22

Two independent reassessments arrived at the same place within a fortnight. One was written on a
machine with no checkout, reading the repo over the GitHub API against a context-lifecycle spec. The
other was measured here, against real transcripts. They agree on the diagnosis and on which verbs to
refuse, and the measurements settle two questions the blind reassessment had to leave open. Both are
folded in below.

#### The active-steward role, refused for the fifth time

An aligned spec proposed an `execute_context_action` tool: retire a rule, filter a hook, compact a
plan, consolidate assets. Each of those writes to a human-authored file.

**DECIDED 2026-09-22 by the owner: not at all, at this stage.** Not as a loom tool, and not as a
separate tool alongside it either. Constraint 8 stands unchanged, and B7c is settled before it
begins: promotion emits a rendered diff, never a write.

This is the fifth time the same role has been examined and declined, which is worth stating plainly
so it is not re-derived a sixth time:

1. Constraint 8 itself, written because a tool in this space once shipped a version that deleted
   users' hand-written hook entries
2. B4, where lane write enforcement was cut as a contradiction of "measures and recommends, the
   human applies"
3. B5, on proposals touching user files: render a diff, nothing more, never
4. B2's pre-compact note, answering whether Loom should take an active role in context lifecycle
   before the question was asked again
5. This decision

The reasoning is kept rather than the conclusion alone: if it is ever wanted, it is a separate tool
consuming Loom's ledger over MCP, never a write path bolted in. Bolting it on requires deleting
constraint 8, and constraint 8 is why this design has held up under scrutiny.

The same decision disposes of `context-mode`'s mechanism, which Loom inherited motivation from and
not implementation. Storing raw assets out of band and handing the model a reference is a durable
secondary copy of whatever was in the session. It would delete the planted-secret test that backs
constraint 6, which is the whole safety argument on any machine that has seen confidential work. It
also overlaps the host's own compaction rather than complementing it. Measured here: native
compaction already achieves a **97.0% mean reduction** across eleven real events, so the mechanism
buys a marginal improvement over something that already exists, at the cost of Loom's strongest
property.

#### The framing moved onto a different metric

Loom measures **spend per run**. The new framing is about **context occupancy**: how fast the window
fills, what fills it, and what that costs. Both come off the same transcripts. They are not the same
number and Loom computes only the first. Nothing in the ledger answers "how full is the window, and
what put it there".

That is the real gap, and it is bigger than any tool name in the spec. It is also the cheapest to
close honestly, because occupancy is derivable from data already ingested, needs no content store, no
write access, and no interrupt.

#### What the measurements settled

Three findings from the personal corpus that the API-only reassessment could not reach.

**Tool output is the thing that fills the window, and one tool dominates it.** Across the lane:

| bucket | result output | leading callers |
|---|---|---|
| file I/O | 32.2 MB | Read 539, Edit 712, Write 317 |
| shell | 2.96 MB | Bash 3,340 |
| MCP server | 1.33 MB | Drive `search_files` 43, `read_file_content` 51 |
| web | 329 KB | WebSearch 88, WebFetch 60 |
| harness / UI | 109 KB | ExitPlanMode, AskUserQuestion, ToolSearch |
| delegation | 26 KB | Agent 21, TaskCreate 38 |

**Read alone is 86% of all tool output**, 539 calls averaging 60KB. This is the actionable half of
occupancy and it was absent from both the spec and the reassessment, which reasoned about how fast
the window fills without asking what fills it.

**Compaction does not need modelling.** An early attempt here inferred compaction from a drop in
`cache_read` and produced 42 candidates, every one of them a `<synthetic>` placeholder record with
zero tokens in every field. A 100% false positive rate. The host already writes the ground truth as a
`compact_boundary` system record carrying `compactMetadata`: `trigger`, `preTokens`, `postTokens`,
`cumulativeDroppedTokens`, `durationMs`, and `preCompactDiscoveredTools`. Read it; do not infer it.

Measured across the lane: 11 compactions, ~6.5M tokens dropped after deduping, **31.4 minutes of
wall clock spent compacting**. That last figure is not a token cost and nothing reports it anywhere.

**Occupancy does not depend on the asset-to-run join (#39).** The reassessment left this open and
suspected it. Confirmed: `tool_use` blocks carry the tool name directly, so tool-output accounting
and compaction pressure need only a tool name and a byte count. The join is per-asset; occupancy
is per-session and time-ordered. They are independent, which means **B7b can go before B7a** for
faster signal, though B7a still gates every claim that uses the word "unused".

#### B7a. Close the three credibility issues (#42, #39, #38) - BUILT 2026-09-22

Nothing else is worth building on an advisor whose central claim it cannot support. Three of five
open issues undercut it: #39 means "unused" cannot be answered, #41 means every promotion rule is
still prose, #38 means retirement can never fire for an asset that exists on disk.

- **#42**, skill discovery: require `<name>/SKILL.md`. Report a flat `.md` in a skills directory as a
  distinct finding, "present but never loadable", rather than counting it as a skill. Worth doing for
  its own sake and not only Loom's: six personal skills on another machine turned out to be dead this
  way, and the same measurement here found eight of nine.
- **#39**, asset-to-run join: record which discovered assets a run touched. Prerequisite for
  every staleness or disuse claim.
- **#38**, retirement condition.

**Measured before designing, not assumed.** String-matching an asset's name or path against
transcript content was tried by hand first and rejected on the same evidence the handover's worked
example warned about: every session's system prompt lists every discovered skill's name and
description whether it fires or not, and a `tool_result` can echo an asset's name back as plain
text from something unrelated (confirmed here: a `Read` of one file quoted a skill's name in
passing, produced by this very machine). Both would have reproduced the near-identical count for
every asset the handover already flagged as a dead end.

**#42 shipped as scoped.** `scanSkillDir` replaces `scanMarkdownDir` for skills only - agents, plans
and memory keep the flat-file convention, which is correct for them. A flat `.md` in a skills
directory is now `KindReference`, not `KindSkill`. Run against this machine's real skills directory:
9 real skills, 1 reference - a leftover `records-management.md` sitting next to its own
`records-management/SKILL.md`, exactly the ambiguity this issue was filed against, on this machine,
not a hypothetical.

**#39 shipped narrower than the credibility-issue framing implied, on the same measurement
discipline B7b used.** Two structured signals only, both read from a `tool_use` block's own `input`
field, never from message text: a `Skill` invocation's `skill` name, and a `Read`/`Edit`/`Write`
call's `file_path`. Agent usage needed no new signal at all - `runs.agent_type` already existed for
B3a's policy attribution, so it answers "was this agent invoked" by itself. Hook usage has no
reliable transcript signal and is out of scope, the same conclusion B7b reached for hook output
volume. A new `asset_usage(run_id, asset_path, uses)` table holds the result, resolved against
the assets table at write time - a skill name or file path matching nothing currently discovered
is dropped, not guessed at.

**#38 turned out smaller than its own issue text once #39 existed.** No new column: retirement's
staleness clock is `last_used` where `asset_usage` has an entry for the path, falling back to
`first_seen` - stable, never reset by a later discovery pass - when it does not. `last_seen` is
untouched and keeps answering its own question (`MarkStaleAssets`, is it on disk). The regression
test reproduces the original bug report exactly: discovery runs three times over weeks on an
asset nothing ever uses, staying on disk (`status = 'active'`) throughout, and the proposal now
fires - which it could never do before, for anything still present.

#### B7b. Occupancy metrics, and `loom context` - BUILT 2026-09-22

Measurement only. Two new tables, both derived metrics and identifiers, so constraint 6 holds:

```
tool_usage(run_id, tool_name, calls, result_bytes)
compactions(run_id, seq, trigger, pre_tokens, post_tokens,
            dropped_cumulative, duration_ms, at)
```

Per session: tool output by tool and by bucket; cumulative input tokens over time and the derived
slope; compaction events, time-to-first-compaction, and wall clock spent compacting; cache-read
share, already computed; per-fire hook output volume, since hook stdout lands in the transcript.
Surfaced through a read-only `loom context` and a `query_ledger` dimension.

Stated limits, up front rather than discovered later:

- **Bytes are not tokens.** Store the measured byte count; label any token figure an estimate. JSON
  and code tokenise worse than prose and a quiet conversion would make every downstream number wrong.
- **No attribution from compaction to cause.** You will see that Read dominates output and that
  compaction fired. "This Read caused it" is not derivable.
- **Resumed sessions carry the prior session's compaction records**, so the same event appears in two
  transcripts. Naive summing inflated the first measurement here by 1.7M tokens. Dedupe on boundary
  identity, and test it: this is the exact shape of the 2.12x over-count bug.

B7b also accrues far faster than per-agent-type run counts, so it is the cheapest route to satisfying
B6d's own evidence gate.

**Built narrower than specified, deliberately.** Tool output by tool and by bucket, and compaction
count/dropped-tokens/wall-clock, are what shipped - both surfaced through `loom context` (read-only,
same reason `loom status` is: this answers "what does the ledger already know", never "go find out")
and a `query_ledger.occupancy` dimension, the latter capped at the top 15 tools per constraint 10
(`loom context` shows the full table). **Cumulative input tokens over time and its slope,
time-to-first-compaction, and per-fire hook output volume did not ship in this pass** - none of them
change what the owner asked for (docs/design.md, "What the owner wants loom to produce"), and adding
them unmeasured would be exactly the scope creep B4's write-enforcement dial was cut for. Revisit
if a finding actually needs them.

Two things the plan above did not spell out, settled during the build:

- **`compactMetadata.cumulativeDroppedTokens` is not what gets summed.** It is cumulative within one
  session lineage, so summing it across several compactions in the same corpus double-counts. Ingest
  sums `preTokens - postTokens` per event instead - not cumulative, safely summable, and exactly what
  "tokens dropped by this compaction" means. See docs/transcript-schema.md, "compact_boundary".
- **The privacy verification this section's own checklist calls for did not exist.** "Ingest a
  fixture containing a planted secret, then grep the database for it" has been in the design doc's
  Verification section since B1, but no test anywhere did it. Built now
  (`internal/ledger/privacy_test.go`, `TestNoContentStored`) - generic over the schema (reads table
  names from `sqlite_master`), so it covers B7b's two new tables and needs no update when a later
  slice adds another.

Run against this machine's own corpus post-build: 11 compactions, 6.82M tokens dropped, 30.6 minutes
wall clock, Read 561 calls / 30.7 MiB (86% of tool output measured on a smaller slice of the same
corpus earlier - see "What the measurements settled" above). Consistent with the hand-measured
figures this section was scoped against, which is the actual test of whether the tables mean what
they claim to.

**A real correctness bug survived that "consistent with the hand-measured figures" check, and
`/code-review high` is what found it, not the tests or the dogfooding above.** `tool_usage` and
`asset_usage` were both written as one row per `(run_id, tool_name)` / `(run_id, asset_path)`,
aggregated at ingest time - with no per-event identity, unlike `compactions`, which had
`boundary_uuid` from the start specifically to dedupe a resumed session's replayed history. Ordinary
`tool_use`/`tool_result` lines turn out to replay the same way compact_boundary records do -
confirmed directly: 325 of 1203 `tool_use` ids shared between one real session and its resumed
continuation - so both tables double-counted every call a resumed session's transcript replayed.
The Read/compaction figures quoted just above are themselves overstated by however much of this
machine's corpus went through a resume, which was not separately measured.

**Fixed by giving both tables the same identity `compactions` already had.** Each `tool_use` block
carries its own globally unique `id`; `tool_usage`/`asset_usage` now store one row per id
(`tool_use_id` as the primary key, `ON CONFLICT DO NOTHING` on insert), aggregated at query time
instead of at write time. A ledger built before this fix has the old, un-deduped shape; `migrate`
drops and rebuilds both tables the next time `loom` opens it; rebuilt at the next `loom report`,
which is the only reasonable trigger to backfill it, and see issue #49 for the gap that already sits
under this exact situation.

The same review pass, verified against the code before acting on any of it, found three more real
issues in the same two features: `retireStaleAssets` never actually checked `runs.agent_type` for
agent-kind assets, contradicting this section's own claim two paragraphs up that agent usage
needs no new signal - a custom agent invoked constantly via the `Agent` tool could be proposed for
retirement as "never used" purely because that tool isn't Skill/Read/Edit/Write. `loom context`
printed nothing about compaction at all when a lane had compactions but no tool-output rows, because
its early return covered both sections at once. `BuildAssetLookup` had no `ORDER BY`, so two
assets sharing a name (a project-level skill overriding a global one - `discover.go` scans both
dirs by design) resolved to whichever row SQLite felt like returning that call. All three fixed
alongside the dedup fix, each with its own regression test reproducing the original failure shape.

#### B7c. Promotion rules as read-only proposals (#41) - BUILT 2026-09-22

Turn the promotion-rules table into code emitting `promote_*` proposals, each carrying evidence and a
rendered diff into Loom's own proposal directory. Same output as the spec's `promote_to_mechanism`,
minus the write.

One rule is already mechanically detectable with no inference at all, and it should be the first:
**a memory file that exists byte-identically across three or more project stores is a cross-project
fact in the wrong mechanism**, and belongs as a reference skill. Measured here: 6 stores, 58 memory
files, 17 exact duplicates, 5 files appearing in three stores each. Hashing only. No threshold, no
recurrence inference, no content retained.

The same pass covers three further structural checks that need no judgement: broken `[[links]]` (3 of
6 here), assets absent from the index that loads them and therefore unreachable (2 here), and
filename-to-slug convention drift, which is what breaks the links (4 here).

Add path-scoped rules to discovery in the same change, since the spec names them and Loom does not
model them. Discovery only: an orphaned rule or an overlapping pair becomes a finding, never a
deletion.

Constraint 10 applies and is satisfied in the same PR, not deferred: state the generation cap, the
dedupe key and the retention rule before merging.

**Built as scoped, with one piece deferred rather than guessed at.** The four structural checks
shipped: `internal/asset.DiscoverAllMemory` walks every project's memory store
(`~/.claude/projects/*/memory`) cross-project - the one place in Loom that needs a wider view than
"home plus the current project", since duplicate detection and index-reachability only mean anything
across stores. Constraint 6 holds throughout: files are hashed and scanned for `[[links]]`, never
retained. Four new proposal kinds route through the existing B5 machinery unchanged
(`propose.Store`/`UpsertProposal`), which is what satisfies constraint 10 without a separate
mechanism: the same `MaxPendingProposals` cap (20) and the same `(kind, subject)`-keyed,
evidence-hash re-raise rule already governed B5's three kinds and now governs these four too.

**Path-scoped rules: discovery not built, and said so rather than guessed.** "The spec names them
and Loom does not model them" was the extent of the direction, with no measured numbers behind it
anywhere in this document, unlike every other B7c check. The natural reading - CLAUDE.md files as a
new asset kind - runs into a real gap: Loom's other discovery is scoped to "home plus the current
project" precisely because those are Claude Code's own standard locations (constraint 2), but a
CLAUDE.md hierarchy lives under a user's own workspace tree, which has no standard root Claude Code
defines. Guessing at one (this machine's own `~/projects/{work,personal,public}` convention, say)
would be encoding one person's layout into the tool, which the design constraints open by forbidding.
Deferred rather than built on a guess - revisit if a concrete shape turns up, the same treatment
semantic staleness already got below.

**Run against this machine's own corpus, all four checks firing for real:** 5 duplicate groups
spanning 3 stores each, 5 broken links, 3 unreachable assets, 27 filename/slug drift cases (17
substantive - e.g. `feedback_working_preferences` renamed to `working-preferences` at some point
without the file following - 10 a systematic underscore-vs-hyphen convention difference, both real
drift by the same definition, not distinguished further since the design doc's own rule draws no
line between them). `loom propose` on the clean, reset local ledger returned exactly 20 - the
generation cap doing its job.

**A real bug found and fixed before this shipped, not after.** The MCP `list_proposals` handler
originally called `os.UserHomeDir()` inside the request handler itself. That made every test of it
non-hermetic: `go test` was scanning whichever machine happened to run the suite's own real,
private `~/.claude/projects/*/memory` files, and `TestListProposalsEmpty` failed outright once B7c's
generator started finding real findings on the developer machine that ran it. Fixed by resolving
`home` once at server construction (`NewServer(db, home)`, threaded from `cmd/loom/serve.go`'s own
`os.UserHomeDir()` call) rather than per-request, and test setup now passes an isolated `t.TempDir()`.
Caught by running the test suite, the same discipline that found B7a/B7b's resumed-session
double-count - the tests are what caught this one, which is the system working as intended.

**Then `/code-review high` was run against the PR before merging, not after, for the first time this
project has done that.** Three more real, verified bugs, all fixed in the same PR:

- **A permission problem on one project's memory store took the whole scan down.**
  `DiscoverAllMemory` only tolerated `os.IsNotExist` on a per-store read, propagating anything else -
  and because this function scans dozens of stores at once, unlike the single-project `Discover()`,
  the blast radius of one bad store was every other store's findings too, and beyond that everything
  `list_proposals`/`loom propose` return, including proposals with nothing to do with memory. A
  regression against constraint 7 in spirit even though the specific pattern (only special-casing
  `IsNotExist`) already existed in `scanMarkdownDir` - it mattered here because of the fan-out, not
  because the pattern itself was new. Fixed: any per-store read error now skips that store, not the
  scan; `MemoryIndex`'s signature dropped its `error` return entirely, since every failure mode it
  can hit now collapses to the same empty-index answer.
- **`detectMemoryDuplicates`'s output order was randomized per call**, from ranging directly over a
  Go map keyed by content hash. Harmless until the total findings across every check exceeded the
  20-proposal cap - the exact situation this machine's corpus produces - at which point which subset
  of duplicate findings actually got a slot depended on map iteration order, so two back-to-back runs
  against identical, unchanged disk state could persist a different set each time. The "not a
  coincidence" claim above was true of the count, not yet of which 20. Fixed with an explicit sort by
  subject before returning.
- **The subdirectory memory convention (`topic/SKILL.md`) was invisible to every B7c check.**
  `DiscoverAllMemory` unconditionally skipped directory entries, while `scanMarkdownDir` - used for
  this exact kind in the single-project `Discover()` path - already treats a subdirectory containing
  its own `SKILL.md` as an equally valid memory asset. A `[[link]]` to such an asset would have
  been reported `broken_link` even though the target genuinely existed. Fixed by mirroring
  `scanMarkdownDir`'s two-shape handling.

Two findings from the same pass considered and not fixed, reasoning kept rather than the conclusion
alone: no caching of the per-call filesystem walk (matches `Discover()`'s own existing, uncached
behaviour exactly - not a regression, and premature caching risks a staleness bug bigger than the
walk cost at this corpus's measured scale); and a `ReadFile`-then-separate-`Open` pair in
`readMemoryFile` that could theoretically observe two different versions of a file edited mid-scan -
a real but vanishingly narrow race, self-correcting on the next run, not worth the complexity of a
single-read refactor for what it would prevent.

**Not in B7c: semantic staleness.** Tested here and rejected on measurement. Extracting the claims an
asset makes and checking whether they still resolve produced 44 candidates and 6 flags, all 6 false
positives: a slash command read as a path, two documentation examples, and two work-machine paths
correctly absent on a personal machine. Separating an assertion from an illustration needs to read for
intent, which needs a model, which is the cost this is meant to reduce. Recorded so it is not retried
without a new idea behind it.

#### B7d. Surfacing: MCP pull, and nothing else

No scheduler, no sweep hook, no session-start nudge. The trigger is the session asking, through
`list_proposals` and the new `query_ledger` dimension. The nudge was cut on measurement, not taste:
20ms budget, 960ms measured cold, and session start is exactly when the binary is cold.

If something must fire proactively, the only place it can go without reopening a cut decision is the
pre-compact hook, already specified as a pointer-writer with a 50ms budget and an
overwrite-never-append rule. It may carry an occupancy summary. It must not gate compaction.

#### B7e. Doc and code hygiene, first commit - BUILT 2026-09-22

Small, and all of it is drift between what the code does and what it says:

- `cmd/loom/main.go`'s usage string and `internal/mcp/doc.go` both say four MCP tools and omit
  `dismiss_proposal`. There are five. `doc.go` also still describes B2 as the current scope.
- The never-write rule is constraint **8**. Four places cite it as constraint 9, which is
  "asset-derived text is data". Residue from the renumbering that produced the append-never-insert
  rule.
- Line 444 still describes a Unix domain socket fast path. `serve.go` is stdio-only and says so.
- 19 em or en dashes remain in this file. The hyphen rule was made a rule elsewhere and this repo has
  not had the sweep. Mechanical, and worth doing in the same pass rather than drifting further.

#### MCP server shape, narrowed further - BUILT 2026-09-23

B7d kept `list_proposals` and the `query_ledger` occupancy dimension on the MCP surface alongside
`get_recommendation`, on the reasoning that pull beats a scheduler. Revisited once more before the
first real trial (installing on a second machine, AMC, to see how it actually changes a session's
behaviour): the MCP surface now carries **`get_recommendation` only**.

`record_outcome` is the one with an actual correctness reason to move, and it is the same reason
B5 already established for `loom propose apply`: it is a write, and "Apply is deliberately CLI-only
... an assistant can call an MCP tool without the human asking. The terminal is where a decision
that changes state belongs" (see B5 above) applies to it word for word - B7d exposing it over MCP
was inconsistent with B5's own rule and should not have happened.

The other two moves (`query_ledger`, `list_proposals`) rest on reasoning, not a measurement, and are
labelled as such rather than stated as settled fact:

- Every one of the four already has a CLI equivalent (`loom report`/`status`/`context`,
  `loom propose`, `loom propose dismiss`, the new `loom record-outcome`), and a live session can run
  any CLI command through its own shell tool. MCP earns its place only for the tool with no such
  substitute: `get_recommendation` needs to fire mid-task, on the hot path, with no human in the
  loop to type a command.
- The assumption, not yet measured: a tool's name and description sit in every session's system
  prompt the plugin is installed into, whether that session ever calls the tool or not, so three
  tools' worth of description text the CLI already covers is a standing cost with nothing measured
  to justify it. If a later trial shows pulling `query_ledger`/`list_proposals` via MCP is worth
  more than that cost, this reverses - unlike the `record_outcome` move above, which does not.

`internal/mcp/server.go`'s `NewServer` dropped to one registration; the now-dead wrapper types
(`LedgerReport`, `ProposalsOutput`, `Proposal`, `DismissInput`/`Output`, `OutcomeInput`/`Output`) and
their handlers were deleted rather than left unused, since the underlying logic they wrapped
(`db.Report`/`Occupancy`, `propose.Generate`/`Store`, `db.DismissProposal`) is exercised directly by
the CLI commands and by `internal/ledger`/`internal/propose`'s own tests - nothing lost coverage.
`NewServer` also dropped its `home` parameter, now unused since `list_proposals` (the one handler
that read it) is gone. Contract version bumped `v0.2.0` -> `v0.3.0` and `plugin.json` to match, per
`internal/mcp/plugin_test.go`'s own rule that the two must move together.

#### The "artifact" noun rename - BUILT 2026-09-23

Loom's artifact (a skill, plan, agent, hook or memory file) collided with the aligned spec's own
artifact (a versioned render block) - the other open item left for the owner rather than decided
during B7. Resolved to **rename, not keep**: shown the actual size of the change (roughly 440
occurrences across 35 Go files, the discovery package, a DB table needing a migration, MCP field
names, and every doc) plus a collision check against loom's own existing vocabulary, the owner chose
**`asset`** over the alternatives checked and ruled out - `resource` collides with MCP's own
first-class "Resources" concept (a real collision with something loom itself could plausibly expose
one day, arguably worse than the one being fixed), `definition` collides with `loom policy render`'s
already-named "agent definitions", and `fixture` collides with `testdata/`'s established meaning.

Mechanical, done in one pass: `internal/artifact` -> `internal/asset`, `Artifact`/`ArtifactRecord`/
`ArtifactRow`/`ArtifactUsageSummary`/`ArtifactLookup`/`ArtifactTouch` and friends -> `Asset...`,
`KindRetireArtifact`/`KindUnreachableArtifact` and their stored kind strings (`retire_artifact` ->
`retire_asset`, `unreachable_artifact` -> `unreachable_asset`) -> `Asset`, the `artifacts`/
`artifact_usage` DB tables and `artifact_path` column -> `assets`/`asset_usage`/`asset_path`, and
every doc. A new `migrateAssetRename` drops the old-named tables on a pre-rename ledger - both are
pure derived caches, rebuilt in full by the next `loom report`/`loom advise`, so nothing is lost;
verified against this machine's own real `~/.loom/loom.db`, backed up first.

**A real bug found by `/code-review high` before merge, not after.** The rename alone left a
pending proposal stored under an old kind string (`retire_artifact`, `unreachable_artifact`)
mis-reported: `propose.TouchesUserFiles` only recognized the new kind strings and defaulted anything
else to `false` - "safe to automate" - which is exactly backwards for a proposal that in fact
touches the user's files. Fixed two ways. `migrateAssetRename` now also deletes any `proposals` row
still carrying an old kind string, since `Generate` never emits one again and the row is permanently
dead. And `TouchesUserFiles` itself flipped its default from fail-open to **fail safe**: it now names
the two kinds that are genuinely safe (`KindPinModel`, `KindRevertPolicy`) and treats everything
else, recognized or not, as touching the user's files - so a kind this function has never heard of,
whenever that happens next, is never silently assumed safe again.

One exception kept as `artifact`, deliberately: `docs/design.md`'s own note that the `coordination`
table "stays in the schema as an empty, unused artifact of the original design" uses the word in its
ordinary English sense (a leftover), not Loom's concept - renaming it would have been wrong, not
thorough. `SECURITY.md`'s mention of "a fetched artifact" (a downloaded release binary) is the same
kind of exception. This document's own historical, dated build entries above (B1 through B7) were
renamed along with everything else, on the view that a pure terminology change is not the same kind
of edit as rewriting a historical *decision* - nothing about what was built or why changes, only its
label, so leaving half the document using the old name for "historical accuracy" would have made it
permanently self-contradictory against the code for no real benefit. This is different in kind from
how the MCP server shape section above treats its own history, where the actual tool inventory
genuinely differed at different points in time and preserving that is the accurate record.

#### Open, and deliberately not decided here

- **Provenance of the dogfooding figures already committed to this doc.** If any were measured on a
  corpus including employer sessions, that needs adjudicating before B6d rather than after. Aggregate
  derived metrics are a far weaker exposure than content and the figures are unattributable on their
  face, so this is probably fine. It should be a decision with a date on it, not an assumption.

#### Order

B7e first, since it is minutes and makes later failures attributable. Then **B7b**, because occupancy
is independent of the join, accrues evidence fastest, and feeds B6d's gate. Then B7a, then B7c, then
B7d last so surfacing earns its place on measurement the way B5's did.

## Verification

- **Ingest correctness.** Replay fixtures and assert computed subagent totals reconcile with the
  totals the transcripts themselves record. If they do not reconcile, the cost model is wrong and
  nothing downstream can be trusted.
- **Memory bound.** Ingest a large synthetic corpus under a hard RSS ceiling, proving streaming.
- **Latency.** Cold start under 20ms. Kill the `loom serve` process mid-session and confirm every
  integration point still returns and the session is unaffected.
- **Write containment.** Run against a read-only fixture tree; assert zero writes outside the
  database and generated-output directory, and assert settings files are never opened for write.
- **Boundary isolation.** Point one boundary's ingest at a fixture belonging to another and assert
  refusal rather than absorption.
- **No content stored.** Ingest a fixture containing a planted secret, then grep the database for it.
  It must not appear.
- **Cold start.** Run every command against an empty ledger; assert useful output, no crashes, no
  silent no-ops.
- **Loop closure.** Apply a policy change, run a second window, confirm the delta is attributed and
  that revert restores the prior policy.

### Added for B7

- **Skill shape (#42).** Point discovery at a fixture tree holding both `foo/SKILL.md` and a flat
  `bar.md`; assert one skill and one "present but never loadable" finding.
- **The join is real, not inferred (#39).** Report over a corpus where a known skill was invoked;
  assert a non-zero use count against a known-dormant one showing zero. State the sample size, per
  constraint 11.
- **Compaction is read, not guessed.** Assert the recorded event comes from the host's
  `compact_boundary` record. A test that reconstructs compaction from a `cache_read` drop must fail:
  that inference was tried and was wrong 42 times out of 42.
- **Resumed sessions do not double-count.** Ingest two transcripts where the second resumes the
  first and carries the same compaction boundary; assert the event is counted once. Same shape as the
  2.12x over-count, so it gets its own fixture. Not compaction-only: `tool_usage` and
  `asset_usage` need the identical test, on the identical shape of evidence (a `tool_use` id
  shared between two runs) - missing here is exactly what let the double-count into both tables
  in the first place, caught only once by code review, not by this list.
- **Bytes stay bytes.** Assert no stored column holds a token estimate. The conversion belongs at the
  display edge, labelled, or downstream arithmetic inherits an error it cannot see.
- **The privacy property survives the migration.** The planted-secret test must pass unchanged after
  B7b's schema change. If B7b makes that test harder to write, the design drifted.
- **Write containment catches the refused verb.** The existing containment test is what stops
  `execute_context_action` arriving by increments. Extend its assertion set to the proposal
  directory and leave it strictly enforced.

## Publishability rules

The tool's only input is transcripts, which on any real machine contain confidential material. So:

- Every fixture is **synthetic and hand-written**. No fixture may be derived from a real transcript,
  even redacted: redaction of a large corpus is not verifiable, invention is.
- Docs, README and examples use invented names only. No real hostnames, repo names, ticket
  references or organisation names.
- The generic mechanisms are publishable. The *instances* (which boundaries exist, which repos, which
  skills) are user configuration and never enter the repo.
- CI runs secret scanning plus a fixture-provenance check on every PR, and both are green before the
  repository is made public. **Secret scanning done (2026-09-12):** gitleaks, as a CI job, not
  GitHub's native secret-scanning toggle - that feature requires the repo to already be public (or
  paid GitHub Advanced Security), confirmed by trying to enable it via the API while private and
  getting "not available for this repository." Relying on it would have made this rule impossible to
  satisfy in the intended order. Fixture-provenance check not yet built.

## Project scaffolding

`LICENSE` (Apache-2.0), `NOTICE`, `README.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`,
`SECURITY.md` with a reporting route, issue and PR templates, `CHANGELOG.md`, semver tags, GitHub
Actions only for test, lint, build and release.

Layout: `cmd/loom/`, `internal/ingest/`, `internal/ledger/`, `internal/selector/`,
`internal/policy/`, `internal/mcp/`, `plugin/`, `testdata/`, `docs/`.

## First session on the personal machine

1. Create the repo, private, Apache-2.0, with the scaffolding above.
2. Write the transcript schema notes into `docs/` first: the JSONL field list at the top of this
   document is the contract everything else depends on, and it is worth pinning down and testing
   against synthetic fixtures before any other code exists.
3. Build B1 only. Ingest, ledger, `report`. Point it at that machine's own history and see whether
   the weighted cost figures reconcile against the recorded `subagent_tokens` values.
4. Stop there and look at the report before designing anything further. If B1's numbers are not
   trustworthy or not interesting, the rest of the design does not deserve to be built.
