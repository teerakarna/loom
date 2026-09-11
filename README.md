# Loom

An artifact lifecycle and cost/routing engine for [Claude Code](https://claude.com/claude-code).

Claude Code accumulates durable artifacts — scratch drafts, memory, skills, plans, hooks, agents,
workflows, plugins — and nothing tracks which are used, which have gone stale, what each one
costs, or which combination fits a given task. Loom reads the session transcripts Claude Code
already writes to disk, builds a local ledger of what actually happened, and reports where cost
and rework go. It measures and recommends; a human applies.

**Status: design complete, build not started.** See [`docs/design.md`](docs/design.md) for the
full design (problem, architecture, promotion rules, phasing) and
[`docs/transcript-schema.md`](docs/transcript-schema.md) for the transcript format Loom ingests.

No content is ever stored — only derived metrics and identifiers — and there is no network
egress from the core. See the design doc's "Design constraints, non-negotiable" section.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
