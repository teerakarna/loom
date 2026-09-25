# Loom - Claude instructions

Read `CONTRIBUTING.md` first for the human-facing contribution process (build/test gate, PR
requirements, where findings get recorded) and `docs/design.md` for the design reasoning and
current status. This file adds what's specific to working here as an agent.

## Before every merge

- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...`, `govulncheck ./...`
  all clean, locally, before pushing - not just relying on CI to catch it. `scripts/ci.sh` runs the
  whole thing (plus gitleaks and the plugin manifest check) in one command.
- Run `/code-review high` on the diff before opening a PR (see CONTRIBUTING.md's own example of
  what green CI alone missed). Re-run it after fixing what it finds: on this repo, a pass has
  repeatedly found a real bug the previous pass's own fix introduced, not just cosmetic notes (the
  CI cost-hygiene PR took five rounds, four of them real - a security override silently undone by
  an action's own internal checkout, a redundant nested Go setup, a missing `!cancelled()`, GitHub's
  own queued-run cancellation semantics, none of it findable by CI alone).
  Two things bound the loop, both found the hard way (issue #84, and a second repo that ran eleven
  `high` passes on one PR under the same rule before this was written down): scope each re-run to
  what the *last fix* touched, not the whole original diff - reviewing the same untouched region
  again and again is exactly how a clean-stop condition with no other bound turns into eleven
  rounds. And treat a pass whose only findings are wording or comments, not behavior, as the actual
  stop signal, not a reason to run again - read the diff yourself at that point and say in the PR
  body that you did, rather than spending another round chasing prose. A pass with zero findings
  still stops immediately, as before; this only changes what counts as "done enough."
- The background review agent sometimes stalls ("no progress for 600s (stream watchdog did not
  recover)"). Retry once or twice at the same effort. If it keeps stalling, proceed on the strength
  of the full automated gate above plus manual verification, and say so plainly in the PR
  description rather than presenting it as a completed review.
- Verify real findings by running the tool, not just by testing - build the binary, point `HOME` at
  a scratch fixture, and check the actual output. Most of what has gone wrong here was found this
  way, not by a test suite that only exercises what it was written to expect.
- Sweep stray em/en dashes to plain hyphens in any file the change touches (global rule,
  `~/.claude/CLAUDE.md`) - `grep -c '—\|–'` before every commit.

## After merge

Sync local `main` from `origin/main` (fetch, then reset --hard) rather than trusting the local
branch state - a squash merge rewrites history the local branch does not have.
