#!/usr/bin/env bash
# Regenerates docs/evidence.md from the local ledger: aggregate numbers only.
# Lane names, proposal subjects and memory file names are deliberately excluded,
# since they reveal private project and memory content. Run by hand, then commit
# the result when you want the numbers to move public.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

status=$(loom status)
policy=$(loom policy)
proposals=$(loom propose 2>/dev/null || true)

num() { printf '%s\n' "$status" | sed -n "s/^  $1 *\([0-9][0-9]*\).*/\1/p" | head -1; }
runs=$(printf '%s\n' "$status" | sed -n 's/^  runs *\([0-9]*\).*/\1/p')
sessions=$(printf '%s\n' "$status" | sed -n 's/^  runs .*(\([0-9]*\) sessions.*/\1/p')
agents=$(printf '%s\n' "$status" | sed -n 's/^  runs .*, \([0-9]*\) agents).*/\1/p')
window=$(printf '%s\n' "$status" | sed -n 's/^  run window *//p')
ready=$(printf '%s\n' "$status" | sed -n 's/^  \([0-9]*\) of \([0-9]*\) agent type.*/\1 of \2/p')
pins=$(printf '%s\n' "$proposals" | grep -c '^  #[0-9]* *pin ' || true)
evidence_rows=$(printf '%s\n' "$policy" | grep '^\*' | sed -E 's/^\* +([^ ]+) +([^ ]+) +effort=([^ ]+) +\[evidence, n=([0-9]+)\].*/| \1 | \2 | \3 | \4 |/')

{
  echo "# Evidence so far"
  echo
  echo "Generated $(date -u +%Y-%m-%d) by \`scripts/evidence.sh\` from one machine's local ledger."
  echo "Aggregate numbers only: no lane names, proposal subjects or memory content."
  echo
  echo "## Corpus"
  echo
  echo "- Runs: ${runs} (${sessions} sessions, ${agents} agent runs)"
  echo "- Run window: ${window}"
  echo "- Agent types with enough runs to displace a default: ${ready:-n/a}"
  echo
  echo "## Measured policies"
  echo
  if [ -n "$evidence_rows" ]; then
    echo "| Agent type | Model | Effort | Runs |"
    echo "|---|---|---|---|"
    printf '%s\n' "$evidence_rows"
  else
    echo "None yet. Every agent type is still on its shipped default."
  fi
  echo
  echo "## Proposals"
  echo
  echo "- Pending model-pin proposals: ${pins}"
  echo "- Proposals that have fired on real data are the gate this project is waiting on; see docs/design.md, B6d."
  echo
  echo "The second half of the loop (a pin applied, then re-measured, then reverted if it regressed) is not yet exercised on real data."
} > docs/evidence.md
echo "wrote docs/evidence.md"
