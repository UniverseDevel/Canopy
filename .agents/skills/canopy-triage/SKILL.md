---
name: canopy-triage
description: On the development machine, analyze test reports and device data packs returned from the work device, turn them into fixes, mocks, profile updates and follow-up testing requests. Use when new files appear in testing/results or testing/devicedata, or when asked to analyze test results.
argument-hint: "[<report path> | --latest | --all-open]"
---

# canopy-triage

Closes the loop on the development side, which has no network devices. Everything
known about real devices comes from `testing/results/` and `testing/devicedata/`, so
treat them as the ground truth and keep them usable as test input.

Read `testing/README.md` and `docs/ARCHITECTURE.md` first.

## 1. Intake

- Find new reports: files in `testing/results/` not yet referenced by any
  `testing/requests/*.md` `## Triage` section (or the path given).
- For each report, check the front matter is complete and the commit it tested exists.
  If the report tested an old commit, note which later commits might already change
  the outcome.
- **Sanitization check before anything else.** Scan the report and every referenced
  data pack for things that look like real secrets or identifiers (private IP ranges
  outside the documentation ranges, hostnames that aren't pseudonyms, hashes, keys,
  certificates). If anything is found, stop and tell the user. Don't copy it anywhere,
  don't quote it.

## 2. Classify each non-pass case

| Class | Meaning | Typical action |
|---|---|---|
| `bug` | Canopy did something wrong | Reproduce locally (2.1), fix, add a regression test |
| `device-quirk` | The device behaves differently than assumed | Update the platform profile and `docs/ARCHITECTURE.md`; add the behavior to the mock |
| `model-gap` | Missing or mismatched YANG models | Fix resolver rules or source config; record in the profile |
| `env` | Work-device environment problem (proxy, TLS, OS) | Fix docs or add a platform-specific code path |
| `test-issue` | The test case or plan was wrong or unclear | Fix the plan; re-request |
| `needs-info` | Not enough evidence to decide | Write a follow-up request asking for exactly what is missing |

### 2.1 Reproduce locally

Prefer reproducing against a mock built from the device data pack:
- Capabilities from `capabilities.json`.
- Get replies from `get/*.json`.
- Error codes from `errors.yaml`.

If the mock can't reproduce it, say so and classify as `needs-info` or
`device-quirk` instead of guessing.

## 3. Use the device data

For each new data pack:
- Copy it (unchanged) into the test fixtures the code uses (for example
  `internal/<pkg>/testdata/devices/<platform>/<version>/`) when it adds a platform or
  version not covered yet, so unit tests and the shadow target run on real shapes.
- Update or create the platform profile: fingerprint (model names, revisions),
  version detection result, supported encodings, origin behavior, error-code
  behavior.
- Record any answers to open questions in `docs/ARCHITECTURE.md` section 11.

## 4. Write back

- Append a `## Triage` section to the request the report answers (or create a request
  stub if the session had none) with a table: case ID, status, class, action, link to
  the commit or file that addresses it.
- For everything that needs another test run, write a new request
  `testing/requests/NNNN-<slug>.md` from the template in that folder's README: the
  commit to test, the exact cases, what changed since the last run, and what evidence
  is needed. Keep requests small; one topic each.
- Summarize for the user: what was learned, what was fixed, what is being asked of the
  next test session.

## Rules

- Never ask the work device for data that the sanitization rules forbid.
- Never edit files under `testing/results/` or `testing/devicedata/`: they're records.
  Corrections go into the triage section or a new request.
- Don't mark anything as fixed until a later report shows it passing on a device, or a
  mock test built from device data proves it; say which one applies.
