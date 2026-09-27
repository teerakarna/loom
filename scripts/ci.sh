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
# golangci-lint and govulncheck both run at CI's own pinned versions
# (golangci-lint v2.13.2, govulncheck v1.8.0 - see ci.yml and CONTRIBUTING.md's
# "Local environment" section) if that's what's on PATH, but nothing here
# enforces the match - a different local version can pass here and fail in
# CI, or the reverse, with no warning beyond the version this prints. This
# script does not install either tool itself, only checks whether it is
# already on PATH and skips with a hint if not (found by code review, before
# this shipped: an earlier version of this comment claimed the script
# installs govulncheck too, which it never did).
# Versions are informational, not enforced, so treat a clean run here as a
# fast local check, not a substitute for watching the real CI run once
# Actions is working again.

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

# required_check NAME CMD... - the build/vet/test shape: no installed()
# branching (go's presence is already checked once, below, before any of
# these run), just a header and a tracked run(), written once instead of
# three copies with the name changed.
required_check() {
	local name="$1"
	shift
	echo "== $name =="
	run "$name" "$@"
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

# optional_check NAME TOOL INSTALL_HINT VERSION_CMD... -- CHECK_CMD... -
# the one pattern all four optional tools below follow (installed?, if so
# print its version and run the real check, if not explain how to get it),
# written once instead of four copies with the tool name changed - found
# necessary by code review, twice: adding a fifth optional tool used to
# mean copying an entire ~10-line block by hand, and a fix to the pattern
# itself (the version/real-check decoupling below, say) had to be
# replicated in all four rather than changed once.
optional_check() {
	local name="$1" tool="$2" hint="$3"
	shift 3
	local version_cmd=()
	while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
		version_cmd+=("$1")
		shift
	done
	shift # drop the -- separator

	echo "== $name =="
	if ! installed "$tool"; then
		echo "$tool not found - skipping (install: $hint)"
		echo
		return
	fi
	# Best-effort and untracked, same reason run() itself never prints a
	# version: a --version flag failing for an unrelated reason must never
	# be mistaken for the real check (which follows, still to come below)
	# having failed. Length-checked first, not just expanded directly
	# (found by code review, before this shipped): expanding an empty
	# array under set -u throws an unbound-variable error on bash <4.4
	# (macOS's own default /bin/bash is 3.2), which would abort the whole
	# script rather than just skip an empty version command - none of the
	# calls below hit this today, but a future optional_check call with no
	# VERSION_CMD would otherwise reintroduce exactly the "one step kills
	# everything after it" failure this file was rewritten to stop doing.
	if [ "${#version_cmd[@]}" -gt 0 ]; then
		"${version_cmd[@]}" || true
	fi
	run "$name" "$@"
}

if ! installed go; then
	echo "go not found - nothing here can run without it (install: https://go.dev/doc/install)"
	exit "$EXIT_INCOMPLETE"
fi

required_check build go build ./...
required_check vet go vet ./...
required_check test go test ./...

optional_check lint golangci-lint "https://golangci-lint.run/welcome/install/" \
	golangci-lint --version -- golangci-lint run ./...

optional_check govulncheck govulncheck "go install golang.org/x/vuln/cmd/govulncheck@v1.8.0" \
	govulncheck -version -- govulncheck ./...

optional_check gitleaks gitleaks "https://github.com/gitleaks/gitleaks#installing" \
	gitleaks version -- gitleaks detect --source . --no-banner

optional_check "plugin manifests" claude "npm install -g @anthropic-ai/claude-code" \
	claude --version -- bash -c 'claude plugin validate ./plugin --strict && claude plugin validate . --strict'

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
