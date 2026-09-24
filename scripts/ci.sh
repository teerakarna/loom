#!/usr/bin/env bash
# Runs the same gate .github/workflows/ci.yml's `ci` job runs, locally.
#
# Exists for the window GitHub Actions can't run at all (a billing
# suspension, an outage) as well as ordinary local use before opening a PR -
# see CLAUDE.md, "Before every merge". Mirrors the workflow step for step;
# keep the two in sync by hand, there is no single source shared between
# YAML and shell for this.
#
# gitleaks (the `secrets` job) is included too, even though that job stays
# split out in CI for its own permission scope - locally there is no
# permission boundary to protect, so running it here costs nothing and
# catches the same class of finding before a push.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

incomplete=0
missing=()

check() {
	if ! command -v "$1" >/dev/null 2>&1; then
		missing+=("$1")
		return 1
	fi
	return 0
}

echo "== build =="
go build ./...

echo "== vet =="
go vet ./...

echo "== test =="
go test ./...

echo "== lint =="
if check golangci-lint; then
	golangci-lint run ./...
else
	echo "golangci-lint not found - skipping (install: https://golangci-lint.run/welcome/install/)"
	incomplete=1
fi

echo "== govulncheck =="
if check govulncheck; then
	govulncheck ./...
else
	echo "govulncheck not found - skipping (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
	incomplete=1
fi

echo "== gitleaks =="
if check gitleaks; then
	gitleaks detect --source . --no-banner
else
	echo "gitleaks not found - skipping (install: https://github.com/gitleaks/gitleaks#installing)"
	incomplete=1
fi

echo "== plugin manifests =="
if check claude; then
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict
else
	echo "claude CLI not found - skipping (install: npm install -g @anthropic-ai/claude-code)"
	incomplete=1
fi

if [ "${#missing[@]}" -gt 0 ]; then
	echo
	echo "Skipped (not installed): ${missing[*]}"
	echo "Everything else passed, but this is not the full gate CI runs - install the above to check it too."
fi

exit "$incomplete"
