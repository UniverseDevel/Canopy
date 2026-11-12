---
name: canopy-collect
description: Collect a sanitized device data pack (gNMI capabilities, version, sample Get replies, error behavior) from a lab or production device on the work device, for Canopy development on a machine without devices. Read-only. Use when asked to collect device data, or when a testing request asks for a data pack.
argument-hint: "[--request NNNN] [--target <host:port>] [--profile <platform-id>]"
---

# canopy-collect

Collects a **device data pack**: everything the development side needs to build
realistic mocks and platform profiles for one device, without access to it.

Read `testing/README.md` first. Its sanitization rules and safety rules apply to every
step here and override anything below.

> **Scope limit:** read-only gNMI only: `capabilities`, `get`, and `subscribe --mode once`.
> No Set, no NETCONF edits, no CLI configuration, no reboots, no changes to jump hosts.
> Never type or echo passwords: the human sets `GNMI_USERNAME` and `GNMI_PASSWORD` in the
> environment, and commands reference those variables.

## Inputs

- A request file in `testing/requests/` if one was given (`--request`). It lists which
  devices, platforms and paths to collect. Otherwise ask the human:
  1. Which device (the human types the address; you only use it in commands)?
  2. Direct, or through a jump host? If a jump host, the human opens the tunnel.
  3. Which platform is it (vendor, OS), as far as they know?
  4. Lab or production? On production, only collect the minimum (steps 1–3 below)
     unless the human explicitly approves more.

## Tooling

Until Canopy has a `collect` command, use gNMIc. Record `gnmic version` in the
manifest. Once Canopy exists, prefer `canopy collect` if the request says so.

Connection flags, assembled once and reused:

```sh
# direct
GNMIC="gnmic -a <device>:<port> -u $GNMI_USERNAME -p $GNMI_PASSWORD --skip-verify --timeout 30s"
# through a tunnel the human opened: ssh -N -L 57400:<device>:<port> <jump-host>
GNMIC="gnmic -a 127.0.0.1:57400 --tls-server-name <device-cert-name> -u $GNMI_USERNAME -p $GNMI_PASSWORD --skip-verify --timeout 30s"
```

Use `--insecure` instead of `--skip-verify` only for plaintext gNMI. Note which one
worked; that's a finding.

## Steps

Work in a scratch folder **outside the repository** first. Only sanitized files are
copied into the repo in step 7.

1. **Capabilities**
   `$GNMIC capabilities --format json > raw/capabilities.json`
   Keep every model name, organization and version exactly as reported.

2. **Encodings.** For each encoding the device advertises among `json`, `json_ietf`,
   `proto`, `ascii`: run one small Get (step 3's version path) with `-e <encoding>` and
   record which ones work.

3. **Version.** Try, in order, and record each attempt (path, origin, encoding, result):
   - `/system/state/software-version`
   - `/components/component/state/software-version`
   - the platform's native path if the request or the human gives one.
   For Cisco NX-OS, also note the revision of `Cisco-NX-OS-device` from Capabilities.

4. **Sample reads.** For each path in the request (or, if none, these defaults where
   the device supports OpenConfig: `/system`, `/interfaces/interface[name=*]/state`,
   `/lldp`, `/network-instances/network-instance[name=*]/config`), run:
   - `get --type config` and `get --type state` separately, `--format json`.
   - If a reply is larger than 20 MB or takes more than 60 s, stop that path and
     record it as `too-large` instead.
   - **Never** read RIB, FIB or AFT paths (`afts`, `rib`, `urib`, `bgp ... adj-rib`),
     even if asked, unless the human confirms the device is a small lab node.

5. **Error behavior.** Run Get on each of these and record the gRPC status code and
   message (not just "failed"):
   - a path that is in a supported model but not configured (for example an unused
     protocol under `/network-instances/.../protocols`);
   - a path that doesn't exist in any model (`/canopy-nonexistent`);
   - a valid path with a wrong origin or prefix.

6. **Subscribe ONCE** on one sample path:
   `$GNMIC subscribe --mode once --path <path> --format json`
   Record whether it works and how long it took.

7. **Sanitize and file the pack.**
   - Apply the rules in `testing/README.md`. Keep the pseudonym map in
     `testing/.redaction-map.local.yaml` (git-ignored) and reuse it across packs.
   - Pack folder: `testing/devicedata/<platform-id>/<version>/<YYYYMMDD>-<dev-pseudonym>/`
     where `<platform-id>` is like `cisco-nxos`, `nokia-srlinux`, `arista-eos`,
     `juniper-junos`, and `<version>` is the version string with spaces and slashes
     replaced by `-` (for example `10.4(3)` stays `10.4(3)`).
   - Files:
     - `manifest.yaml` from `testing/templates/devicedata-manifest.yaml`
     - `capabilities.json`
     - `version.yaml` (all attempts from step 3)
     - `encodings.yaml` (step 2)
     - `get/<path-slug>.<config|state>.json` (step 4; slug = path with `/` → `_`,
       brackets and `=` kept, `*` → `ALL`)
     - `errors.yaml` (step 5)
     - `subscribe-once.yaml` (step 6)
     - `notes.md`: anything surprising, in plain sentences.

8. **Report.** Write a short report in `testing/results/` using
   `testing/templates/report.md`, with the collection steps as test cases
   (`COLLECT-01` … `COLLECT-06`), and the redaction summary at the end.

## Done when

- The pack folder has every file listed above, or the report says why one is missing.
- `grep` over the pack for the real hostnames, IPs, usernames and the words
  `password`, `secret`, `key` finds nothing unexpected (state what you searched for).
- The human tester has reviewed the diff.
