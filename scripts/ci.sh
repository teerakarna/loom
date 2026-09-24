#!/usr/bin/env bash
# Runs the same checks .github/workflows/ci.yml's `ci` job runs, locally.
#
# Exists for the window GitHub Actions can't run at all (a billing
# suspension, an outage) as well as ordinary local use before opening a PR -
# see CLAUDE.md, "Before every merge". Same checks as the workflow, not the
# same steps in the same order - ci.yml runs the plugin-manifest check
# first, before Go is even set up, for a cache-isolation reason that does
# not apply to a developer's own already-persistent machine, so this runs
# it last instead. Keep the two in sync by hand when a check itself
# changes; there is no single source shared between YAML and shell for
# this, only the same intent.
#
# Every check below runs regardless of whether an earlier one failed,
# mirroring ci.yml's own `if: ${{ !cancelled() }}` on every step after
# checkout - a build error and an unrelated lint issue both get reported in
# the same run here too, not one hidden behind the other until a second
# pass (found by code review, before this shipped: an earlier version used
# `set -e`, so a build failure locally stopped before lint/govulncheck/
# gitleaks/plugin-manifest ever ran, silently losing the guarantee CI
# itself was just fixed to provide).
#
# gitleaks (the `secrets` job) is included too, even though that job stays
# split out in CI for its own permission scope - locally there is no
# permission boundary to protect, so running it here costs nothing and
# catches the same class of finding before a push.
#
# Exit codes: 0 clean, 1 one or more real checks failed, 2 nothing failed
# but one or more tools were not installed, so the gate this ran was
# incomplete - distinct from 1 so a caller checking $? alone, not just
# reading the output, can tell "your code has a problem" apart from "this
# machine is missing a tool" (found by code review, before this shipped).
# 1 takes priority over 2 when both apply.
#
# golangci-lint, govulncheck and gitleaks run at whatever version is on
# PATH, unlike CI, which pins each one - only the Go toolchain itself is
# actually shared (go-version-file: go.mod). Versions are printed, not
# enforced: a mismatch can pass here and fail in CI (different lint rules,
# different leak signatures), so treat this as a fast local check, not a
# substitute for watching the real CI run once Actions is working again.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

EXIT_FAILED=1
EXIT_INCOMPLETE=2

failed=0
incomplete=0
failed_checks=()
missing=()

# run NAME CMD... - executes CMD, printing a header before and a blank line
# after, and records NAME as failed without stopping the rest of the
# script (see the file header on why that matters).
run() {
	local name="$1"
	shift
	echo "== $name =="
	if ! "$@"; then
		failed_checks+=("$name")
		failed=1
	fi
	echo
}

# installed TOOL - reports whether TOOL is on PATH, recording it as missing
# (and the whole run as incomplete) if not.
installed() {
	if command -v "$1" >/dev/null 2>&1; then
		return 0
	fi
	missing+=("$1")
	incomplete=1
	return 1
}

run build go build ./...
run vet go vet ./...
run test go test ./...

if installed golangci-lint; then
	run lint bash -c 'golangci-lint --version && golangci-lint run ./...'
else
	echo "== lint =="
	echo "golangci-lint not found - skipping (install: https://golangci-lint.run/welcome/install/)"
	echo
fi

if installed govulncheck; then
	run govulncheck bash -c 'govulncheck -version && govulncheck ./...'
else
	echo "== govulncheck =="
	echo "govulncheck not found - skipping (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
	echo
fi

if installed gitleaks; then
	run gitleaks bash -c 'gitleaks version && gitleaks detect --source . --no-banner'
else
	echo "== gitleaks =="
	echo "gitleaks not found - skipping (install: https://github.com/gitleaks/gitleaks#installing)"
	echo
fi

if installed claude; then
	run "plugin manifests" bash -c 'claude plugin validate ./plugin --strict && claude plugin validate . --strict'
else
	echo "== plugin manifests =="
	echo "claude CLI not found - skipping (install: npm install -g @anthropic-ai/claude-code)"
	echo
fi

if [ "$incomplete" -eq 1 ]; then
	echo "Skipped (not installed): ${missing[*]}"
	echo "The checks that did run are accurate, but this is not the full gate CI runs - install the above to check it too."
	echo
fi

if [ "$failed" -eq 1 ]; then
	echo "Failed: ${failed_checks[*]}"
	exit "$EXIT_FAILED"
fi

if [ "$incomplete" -eq 1 ]; then
	exit "$EXIT_INCOMPLETE"
fi
