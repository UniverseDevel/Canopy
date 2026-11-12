# Testing requests

Requests are written on the development side and picked up on the work device by the
`canopy-test` or `canopy-collect` skill. Number them sequentially (`0001`, `0002`, …)
and never reuse a number. A request is open until a report in `testing/results/`
references it; its outcome is recorded in a `## Triage` section appended here.

## Template

```markdown
# NNNN — <short title>

- **Type:** test | collect | both
- **Commit/branch to test:** <sha or branch, or "n/a" for collect-only>
- **Plan cases:** <M1-CONN-01, M1-CONN-02, … | none>
- **Devices:** <platforms/versions wanted, e.g. "any NX-OS 10.4+ lab switch", "SR Linux containerlab">
- **Writes needed:** no | yes: <which cases, why>

## Why

<What the development side needs to learn and why it matters.>

## What changed since the last run

<Commits or behavior relevant to this request, or "first run".>

## Instructions

<Anything beyond the plan: extra paths to collect, debug flags, specific UI steps.>

## Evidence needed

<Exactly what must be in the report or pack to close this request.>
```
