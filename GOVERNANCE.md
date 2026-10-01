# Governance

## Today

Loom has one maintainer ([@teerakarna](https://github.com/teerakarna)), who makes final calls on
scope, design, and releases. Design decisions and their rationale live in
[`docs/design.md`](docs/design.md) rather than in any one person's head, specifically so the
project doesn't depend on that continuing to be true.

This is a level of process proportionate to a single-maintainer, pre-1.0 project, not a
placeholder for something heavier that hasn't been written yet. It'll grow if and when the project
does.

## How decisions get made

- Day-to-day implementation choices: made directly, explained in commit messages and PR
  descriptions.
- Architectural or design-constraint changes (the numbered constraints in
  [`docs/design.md`](docs/design.md), the kind covered in [CONTRIBUTING.md](CONTRIBUTING.md)):
  discussed in an issue before a PR, since these are exactly the decisions the project's
  evidence-based thesis depends on.
- Anything affecting the project's stated scope (see `docs/design.md`'s delivery slices and
  explicit non-goals): same, issue first.

## If a second maintainer joins

The plan, not yet needed: shared write access, a documented review requirement in branch
protection (currently unset, since a review requirement with one maintainer would just block every
merge), and this document gets rewritten to describe the real process rather than the
single-person one above.

## Security

Handled separately and privately, see [SECURITY.md](SECURITY.md), not this document or public
issues.
