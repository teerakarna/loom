# Security Policy

## Reporting a Vulnerability

Please report security issues privately using
[GitHub Security Advisories](https://github.com/teerakarna/loom/security/advisories/new) for this
repository, rather than opening a public issue.

If you're unable to use Security Advisories, email 21040807+teerakarna@users.noreply.github.com
with a description of the issue and steps to reproduce.

Please do not disclose the issue publicly until it has been addressed.

## Scope

Loom reads Claude Code session transcripts, which on any real machine contain confidential
material. That single fact defines the threat model: the sensitive thing is already on disk, and
Loom's job is to derive metrics from it without retaining, moving or re-emitting any of it.

The areas of real security interest, most serious first:

- **Anything that could put message content, rather than derived metrics and identifiers, into the
  ledger.** This is design constraint 6 and it is the property that makes Loom safe to point at a
  confidential corpus at all. Enforced by a test that ingests a fixture containing a planted secret
  and then greps every table for it (`internal/ledger/privacy_test.go`).
- **Network egress from the core.** There is none, as an invariant rather than a default. A change
  that introduces any is a vulnerability, not a feature.
- **The ledger file itself.** Constraint 6 keeps content out; it does not keep *identifiers* out,
  and transcript paths encode project directory names. The ledger is therefore machine-local by
  rule (constraint 12) and must never be synced, committed or backed up to a shared location.
- **Asset-derived text re-served over MCP.** A skill, agent or plan's name and description are
  read verbatim off disk and returned through `get_recommendation` into whatever session asked,
  which is untrusted content entering another agent's context (constraint 9). It is length-capped,
  every affected field says in its own schema that it is data rather than an instruction, and an
  advisory `suspicious` flag marks obviously injection-shaped text. The flag is never a filter and
  its absence is not a guarantee.
- **The plugin's binary resolution.** `plugin/bin/loom-mcp` resolves an already-installed `loom`
  via `$LOOM_BIN`, then PATH, then the locations the documented install methods use. It does not
  download or execute a fetched artifact. Releases are not yet signed, so a user is trusting
  whatever they installed themselves; signing is tracked in `docs/design.md` as B6a and is a
  prerequisite for distributing binaries to strangers.

`loom serve` speaks MCP over stdio only, as a subprocess of the client that spawned it. There is no
daemon, no socket and no listening port.
