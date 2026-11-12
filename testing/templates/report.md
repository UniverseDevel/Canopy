---
# Canopy test report. Fill in every field; use "unknown" rather than leaving one out.
report: YYYYMMDD-<request-or-plan>-<slug>
request: "NNNN"            # or "none"
plan: M1                   # plan the case IDs come from
tested_commit: <full git sha>
working_tree_clean: true   # false: list local changes under Tester notes
canopy_version: <output of `canopy version`, or "not built">
date: YYYY-MM-DD
tester: human+agent        # no names
environment:
  os: <macOS 15.x | Windows 11 24H2 | ...>
  arch: <arm64 | amd64>
  go: <go version>
  browser: <name version | n/a>
  gnmic: <version | n/a>
access:
  path: <direct | jump-host x1 | jump-host x2>
  tls: <plaintext | tls-skip-verify | tls-ca | mtls>
devices:                   # pseudonyms only
  - id: dev-01
    platform: <cisco-nxos | nokia-srlinux | arista-eos | juniper-junos | ...>
    version: <exact version string>
    kind: <lab | production>
    datapack: testing/devicedata/<platform>/<version>/<pack>   # or "none"
summary:
  pass: 0
  fail: 0
  blocked: 0
  skipped: 0
---

# <one-line title of the session>

## Cases

<!-- Repeat this block for every case in scope, in plan order. Keep the field names. -->

### M1-XXXX-NN — <case title>

- **Status:** pass | fail | blocked | skipped
- **Device:** dev-01 | n/a
- **Expected:** <copied from the plan>
- **Actual:** <what happened, concretely>
- **Steps run:**
  ```sh
  <exact commands, secrets as $VARS, hosts as pseudonyms>
  ```
- **Evidence:**
  ```text
  <trimmed output or log lines; keep the first error and its context whole>
  ```
- **Observations:** <optional: anything notable even on pass>

## Tester notes

<Free text. Hypotheses must be labeled "Hypothesis:". Local changes to the working
tree, if any. UI impressions.>

## Redaction summary

| Kind | Count | Notes |
|---|---|---|
| hostname | 0 | |
| ipv4 | 0 | |
| ipv6 | 0 | |
| secret | 0 | |
| username | 0 | |
| serial/mac | 0 | |
| org text | 0 | |

Searched for before handover: <list of terms/patterns checked, without the real values>
