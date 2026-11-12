---
name: canopy-test
description: Run a Canopy test session on the work device alongside the human tester - build Canopy, execute test cases from a testing request or milestone plan against lab devices, and write a structured, sanitized report for the development side. Use when asked to test Canopy, run a test plan or work through a testing request.
argument-hint: "[--request NNNN] [--plan M1] [--cases M1-CONN-01,M1-TREE-02]"
---

# canopy-test

Runs a test session and produces a report the development side can act on without
asking follow-up questions.

Read `testing/README.md` first. Its sanitization and safety rules apply to every step
and override anything below.

> **Scope limit:** read-only against devices unless a test case explicitly needs a write
> **and** the human approves that specific write **and** the device is a lab device.
> Never type, echo or store passwords; the human sets `GNMI_USERNAME`/`GNMI_PASSWORD`.
> Don't modify Canopy's source code. If something is broken, report it; fixes happen on
> the development side.

## 1. Pick the scope

- With `--request NNNN`: read `testing/requests/NNNN-*.md`. It names the commit or
  branch to test, the test cases, devices and anything extra to collect.
- With `--plan` / `--cases`: use `testing/plans/<plan>.md`.
- Otherwise list the open requests (those without a matching file in
  `testing/results/`) and ask the human which one to run.

Confirm with the human, in one message: commit to test, test cases, devices
(pseudonyms only in anything you write down), and whether any case needs a write.

## 2. Record the environment

Capture before testing, for the report's front matter:
- OS and version, CPU architecture.
- `git rev-parse HEAD` and `git status --short` (must be clean, or list local changes).
- `go version`, and the Canopy version output (`canopy version`).
- Browser name and version if the web UI is tested.
- How devices are reached: direct or jump host (count of hops), TLS mode.

## 3. Build

Follow the build steps in the repository's `README.md` (or the request, if it gives
them). Record the exact commands and whether they succeeded. A failed build is a
`fail` for case `BUILD-01`; stop and report.

## 4. Execute test cases

For each case, in order:

1. Restate the case ID and expected result from the plan.
2. Do the steps. For UI steps, the human usually drives; you record what they report
   and what you can verify (logs, files, copied output). For CLI steps you run the
   commands.
3. Capture evidence:
   - exact commands, with secrets as variables and real hosts as pseudonyms;
   - relevant output, trimmed to what matters (keep the first error and its context
     whole);
   - Canopy logs for the case's time window (run Canopy with debug logging if the
     plan says so);
   - screenshots only when the human provides them, saved under
     `testing/results/<report-name>/` after the human confirms they show nothing
     sensitive.
4. Set the status:
   - `pass`: matches the expected result exactly.
   - `fail`: doesn't match. Say what differed, concretely.
   - `blocked`: couldn't run (dependency failed, device unavailable). Say why.
   - `skipped`: deliberately not run. Say why.
5. Add **observations** whenever something is interesting even if the case passed:
   slowness, odd device behavior, confusing UI, unexpected warnings. These are often
   more valuable than the status.

If the request asks for device data, run the `canopy-collect` skill for those devices
after the test cases, and reference the packs in the report.

## 5. Write the report

- File: `testing/results/<YYYYMMDD>-<request or plan>-<slug>.md`, from
  `testing/templates/report.md`. Fill in every front-matter field.
- One section per test case, in plan order, using the exact block format from the
  template so it can be parsed.
- Sanitize per `testing/README.md`. End with the redaction summary.
- Don't speculate about causes in the case blocks. If you have a hypothesis, put it in
  the `## Tester notes` section, labeled as a hypothesis.

## 6. Hand over

- Show the human a summary: counts per status, the failures in one line each, and the
  redaction summary.
- The human reviews the diff, then commits on `testing/<YYYYMMDD>-<request>` and pushes
  (or archives `testing/results/` and `testing/devicedata/`, as described in
  `testing/README.md`).
