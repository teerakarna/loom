# Loom as a Claude Code plugin

This directory is the plugin. It registers Loom's MCP server with Claude Code and nothing else.

## Install

```
/plugin marketplace add teerakarna/loom
/plugin install loom@loom
```

The plugin does not contain Loom. Install the binary separately - see the main
[README](../README.md#install): `go install ...@latest`, or build from a clone.

Then restart Claude Code. `/mcp` should list `loom` as connected, and `loom` gains one tool:
`get_recommendation`. Everything else (cost reports, proposals, recording an outcome) is
CLI-only - a session that needs one runs the `loom` binary directly via its shell tool.

Installing the plugin does not ingest anything by itself. The first finished turn after install
does, through the hook below; or run `loom report` once by hand.

## What it ships, and what it does not

| | |
|---|---|
| MCP server registration | yes, `.mcp.json` |
| Manifest | yes, `.claude-plugin/plugin.json` |
| Hooks | one: a `Stop` hook that runs `loom ingest` in the background, see below |
| Skills | none |
| Commands | none |
| The binary | no |

## The ingest hook

`hooks/hooks.json` registers one `Stop` hook, async, running `bin/loom-ingest` after each turn. It
calls `loom ingest`, which brings the ledger up to date and prints nothing: a no-op takes about 10ms
and a first ingest of a large history under a second. It always exits 0, does nothing if the binary
is missing, has its own two-minute cap, and never writes anywhere but Loom's own ledger. Set
`LOOM_NO_HOOK=1` to turn it off without uninstalling. It ingests only; proposals, pins and applies
stay manual. Why `Stop` and not `SessionEnd` or `SessionStart`: `docs/design.md`, "Unattended ingest".

## What was cut

The design doc used to say this plugin would ship "the MCP server registration, the optional hooks,
the Advisor skill and a manifest". Two of those four were cut. The session-start hook was cut
on measurement, it ran 48x over its latency budget for a nudge nobody asked for. The Advisor skill
would have wrapped `list_proposals`, `dismiss_proposal` and `loom propose apply`, which already do
that job through surfaces that exist; a skill on top would have been a third way to do the same
thing. Neither was replaced, so the plugin is thinner than it was specified to be, and this table is
the honest version. The ingest hook is a different thing from the cut nudge: it prints nothing.

## Why `bin/loom-mcp` instead of just `"command": "loom"`

PATH. Claude Code launches an MCP server with the environment it was launched with. `go install`
puts the binary in `~/go/bin`, which is on an interactive shell's PATH and is not on the PATH a
desktop app inherits from the launcher. Registering the bare name works from a terminal and fails
from the app, the worst available failure mode, because it looks like a Loom bug rather than a
missing directory.

The wrapper checks `$LOOM_BIN`, then PATH, then the places the documented install methods actually
put it, and if it finds nothing it says so in words rather than exiting silently. Set `LOOM_BIN` to
override.

## Working on it locally

```
claude plugin validate ./plugin --strict     # schema only, see below
claude plugin marketplace add ./             # from a checkout, note the trailing slash
claude plugin install loom@loom
claude mcp list                              # plugin:loom:loom should say Connected
```

`claude plugin marketplace add .` is rejected as an invalid source; `./` is accepted. Remove it
again with `claude plugin marketplace remove loom`.

`validate` checks the manifest's shape and nothing else. A command pointing at a file that does not
exist, a wrapper with no executable bit, and a version disagreeing with the server all pass it,
each was tried. Those three are covered by `internal/mcp/plugin_test.go` instead.

## What it does not do

It does not fetch or verify a binary. An earlier version of this file said the install step would
"fetch the matching platform binary and verify it, refusing an unverified one rather than falling
back". That describes something worth building, but it requires signed releases to verify against,
and there are none yet (B6a). Shipping a fetch step with no signature to check would be the shape of
supply-chain safety without the substance, so the plugin resolves a binary you installed yourself
and is explicit that this is what it is doing.
