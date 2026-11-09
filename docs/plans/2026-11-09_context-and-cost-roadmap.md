# Context and cost roadmap

Status: active. Tracking: #142.

## Question

Should loom grow into a usage and cost tool like ccusage, or should loom stay what it is and close
the gaps that matter for its own purpose: artifact lifecycle, context optimisation and cost
analysis?

## Answer

Stay what it is. Build a short list that only loom can do well, and refuse the features that would
turn it into a weaker usage reporter.

## What the comparison found

ccusage reads local agent logs and prints daily, weekly, monthly and session reports in dollars,
for about twenty agents. It is the most used tool for that job.

Loom already does better on what it is for:

- **A durable ledger.** Claude Code deletes transcripts after `cleanupPeriodDays` (30 by default).
  On one machine the ledger held 164 runs whose transcripts were already gone; a tool that reads
  the files on disk cannot report those.
- **De-duplication.** In a sampled 13 MB transcript, 54% of assistant entries repeated an earlier
  response and carried 55% of the raw token total. Loom de-duplicates by `message.id`, including
  across resumed-session replays (#6).
- **Attribution** by lane, agent type and asset, with no message content stored and no network.

Loom is weak on money. Its cost is a relative proxy with the same token-kind weights for every
model, so cross-model comparisons read as money but are not (#141). Transcripts carry
`service_tier`, `speed` and `inference_geo` but no recorded dollar cost, so any price has to come
from outside the transcript.

## Plan, in order

1. **Label cost honestly** (#141, option 1). Say "weighted tokens" wherever a per-model share is
   printed. Reword the "272x" comment in `internal/selector/recommend.go`, which measures workload as
   well as price. Small, and removes the misreading now.
2. **Time roll-ups.** Daily, weekly and monthly sections in `loom report`, from timestamps the
   ledger already holds. No new data.
3. **Prices from a user-supplied file.** Optional and off by default. Loom ships no price table and
   makes no request. It covers the tiers that change the answer: 1-hour cache writes, long context
   and fast mode. Output is labelled as an estimate (constraint 11), since subscription and
   contracted pricing differ from list price. Cross-model views can use it; same-model policy
   baselines do not need it.
4. **Per-asset context cost.** What each skill, plugin, MCP server and instruction file costs per
   session against how often it is used. This is the view no usage reporter gives, and it feeds
   `retire_asset` with a cost figure and a reversible option (#138).
5. **Windows.** Add it to the release targets, replace the few `/`-joined paths with
   `filepath.Join`, and add a non-Linux CI leg. The binary is already pure Go with `CGO_ENABLED=0`
   and a pure-Go SQLite driver, so this is small.

## Not doing

- **Adapters for other agents or LLMs.** That is a usage reporter's job, and it would dilute the
  asset model loom is built on.
- **Live blocks or a status line.** Claude Code already has a native cost field and `/usage`.
- **A TUI or live dashboard.** `docs/design.md`, "Surfacing": a loom that owned the terminal would
  compete with the thing it observes. A better static `loom report` covers the need.
- **Team dashboards.** Use OpenTelemetry export.
- **Fetching a price feed.** Breaks the no-network constraint, and a fetched table would claim a
  precision that real pricing does not have.

## Open decision

Whether loom should print dollars at all, even from a user-supplied file. The risk is false
precision. Step 3 is designed so the answer can stay "no" without blocking steps 1, 2, 4 and 5.

## First check before building

Cross-check loom's weighted totals against ccusage once, on a pinned version in `--offline` mode, to
see how far the two diverge and whether the difference is de-duplication, model weighting or both.
