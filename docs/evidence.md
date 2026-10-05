# Evidence so far

Generated 2026-10-05 by `scripts/evidence.sh` from one machine's local ledger.
Aggregate numbers only: no lane names, proposal subjects or memory content.

## Corpus

- Runs: 537 (8 sessions, 529 agent runs)
- Run window: 2026-06-16T20:33:15Z to 2026-10-01T05:25:42Z
- Agent types with enough runs to displace a default: 2 of 4

## Measured policies

| Agent type | Model | Effort | Runs |
|---|---|---|---|
| fork | claude-sonnet-5 | medium | 203 |
| general-purpose | claude-sonnet-5 | medium | 306 |

## Proposals

- Pending model-pin proposals: 2
- Proposals that have fired on real data are the gate this project is waiting on; see docs/design.md, B6d.

The second half of the loop (a pin applied, then re-measured, then reverted if it regressed) is not yet exercised on real data.
