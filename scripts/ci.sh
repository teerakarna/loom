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
# gitleaks here scans whatever history the local clone actually has - a
# shallow clone narrows the window the same way it would for any local
# gitleaks run. ci.yml's own secrets job uses fetch-depth: 0 to scan full
# history; this script does not force that locally, so it is not a
# substitute for that job specifically finding something committed outside
# a shallow clone's window.
#
# Every check below runs regardless of whether an earlier one failed,
# mirroring ci.yml's own if: ${{ !cancelled() && steps.checkout.outcome ==
# 'success' }} on every step after checkout - a build error and an
# unrelated lint issue both get reported in the same run here too, not one
# hidden behind the other until a second pass (found by code review, before
# this shipped: an earlier version used `set -e`, so a build failure
# locally stopped before lint/govulncheck/gitleaks/plugin-manifest ever
# ran, silently losing the guarantee CI itself was just fixed to provide).
#
# Exit codes: 0 clean, 1 one or more real checks failed, 2 nothing failed
# but one or more tools were not installed, so the gate this ran was
# incomplete - distinct from 1 so a caller checking $? alone, not just
# reading the output, can tell "your code has a problem" apart from "this
# machine is missing a tool" (found by code review, before this shipped). 1
# takes priority over 2 when both apply. go itself is checked first and is
# not optional the way the other four tools are - nothing here can run
# without it, so a missing go exits 2 immediately rather than being
# misreported as build/vet/test failing.
#
# golangci-lint runs at CI's own pinned version (v2.13.2 in ci.yml) if
# that's what's on PATH, but nothing here enforces the match - a different
# local version can pass here and fail in CI, or the reverse, with no
# warning beyond the version this prints. govulncheck is not pinned by
# either side: both ci.yml and this script install it at @latest, so there
# is nothing to compare it against. Versions are informational, not
# enforced, so treat a clean run here as a fast local check, not a
# substitute for watching the real CI run once Actions is working again.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

EXIT_FAILED=1
EXIT_INCOMPLETE=2

failed_checks=()
missing=()

# run NAME CMD... - executes CMD, then a blank line after, and records
# NAME as failed without stopping the rest of the script (see the file
# header on why that matters). Prints no header of its own - the caller
# already printed one, since it also needs one for the "not installed"
# branch run() is never called in. Never prints a version or otherwise
# decides pass/fail from anything but CMD's own exit status - a caller
# that wants version info prints it separately, first, ignoring its own
# exit code, so a --version flag failing for an unrelated reason can
# never be mistaken for the real check failing (found by code review,
# before this shipped).
run() {
	local name="$1"
	shift
	if ! "$@"; then
		failed_checks+=("$name")
	fi
	echo
}

# installed TOOL - reports whether TOOL is on PATH, recording it as missing
# if not. Does not print anything itself; the caller decides what a
# missing tool means for that section.
installed() {
	if command -v "$1" >/dev/null 2>&1; then
		return 0
	fi
	missing+=("$1")
	return 1
}

if ! installed go; then
	echo "go not found - nothing here can run without it (install: https://go.dev/doc/install)"
	exit "$EXIT_INCOMPLETE"
fi

echo "== build =="
run build go build ./...

echo "== vet =="
run vet go vet ./...

echo "== test =="
run test go test ./...

if installed golangci-lint; then
	echo "== lint =="
	golangci-lint --version || true
	run lint golangci-lint run ./...
else
	echo "== lint =="
	echo "golangci-lint not found - skipping (install: https://golangci-lint.run/welcome/install/)"
	echo
fi

if installed govulncheck; then
	echo "== govulncheck =="
	govulncheck -version || true
	run govulncheck govulncheck ./...
else
	echo "== govulncheck =="
	echo "govulncheck not found - skipping (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
	echo
fi

if installed gitleaks; then
	echo "== gitleaks =="
	gitleaks version || true
	run gitleaks gitleaks detect --source . --no-banner
else
	echo "== gitleaks =="
	echo "gitleaks not found - skipping (install: https://github.com/gitleaks/gitleaks#installing)"
	echo
fi

if installed claude; then
	echo "== plugin manifests =="
	run "plugin manifests" bash -c 'claude plugin validate ./plugin --strict && claude plugin validate . --strict'
else
	echo "== plugin manifests =="
	echo "claude CLI not found - skipping (install: npm install -g @anthropic-ai/claude-code)"
	echo
fi

if [ "${#missing[@]}" -gt 0 ]; then
	echo "Skipped (not installed): ${missing[*]}"
	echo "The checks that did run are accurate, but this is not the full gate CI runs - install the above to check it too."
	echo
fi

if [ "${#failed_checks[@]}" -gt 0 ]; then
	echo "Failed: ${failed_checks[*]}"
	exit "$EXIT_FAILED"
fi

if [ "${#missing[@]}" -gt 0 ]; then
	exit "$EXIT_INCOMPLETE"
fi
