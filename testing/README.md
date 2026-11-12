# Canopy testing loop

Canopy is developed on a machine **without network devices**. Testing happens on a
**work device** that can reach labs and devices, with an AI agent that runs tests
alongside a human tester. Everything moves between the two sides through this folder.

```text
 development machine                          work device
 (code, mocks, analysis)                      (devices, human + test agent)

   testing/requests/NNNN-*.md   ───────────▶  what to test or collect next
                                              │
                                              ▼  canopy-collect / canopy-test skills
   testing/results/*.md         ◀───────────  test reports
   testing/devicedata/**        ◀───────────  sanitized device data packs
   │
   ▼  canopy-triage skill
   fixes, mocks, profiles, next requests
```

## Folder layout

| Path | Written by | Contents |
|---|---|---|
| `requests/NNNN-<slug>.md` | development side | A test or collection request with an ID, scope and acceptance criteria |
| `results/<date>-<request>-<slug>.md` | work device | One report per test session, using `templates/report.md` |
| `devicedata/<platform>/<version>/<pack>/` | work device | One data pack per device and collection run, using `templates/devicedata-manifest.yaml` |
| `plans/M<n>.md` | development side | Test cases per milestone, with stable IDs |
| `templates/` | development side | Report and manifest templates |

## Rules for everyone

1. **Sanitize before anything is committed or copied off the work device.** See below.
   If in doubt, leave it out and say so in the report.
2. **Read-only by default.** Only gNMI Capabilities, Get and Subscribe ONCE. Any Set,
   any NETCONF edit, and anything that changes a device needs the human tester's
   explicit approval for that specific action, and only on lab devices.
3. **Facts, not interpretation.** Reports record what was run and what happened,
   with exact commands (secrets replaced by variables) and outputs. Analysis happens on
   the development side.
4. **Stable IDs.** Test cases are referenced by plan ID (`M1-CONN-01`); requests by
   number (`0001`). Never renumber.
5. **Nothing silently skipped.** Every test case in scope ends as `pass`, `fail`,
   `blocked` or `skipped`, with a reason for the last two.

## Sanitization

Results leave the work environment, so they must contain nothing sensitive and nothing
that identifies the organization, sites or people.

**Remove entirely** (replace with `<REDACTED:kind>`):
- Passwords, password hashes, tokens, API keys, SSH keys, TLS private keys and
  certificates, SNMP communities, TACACS/RADIUS keys, BGP/OSPF/IS-IS auth keys,
  MACsec keys, any `secret`/`key`/`password` leaf.
- Usernames other than generic lab accounts.

**Replace consistently with pseudonyms** (the same real value always maps to the same
pseudonym within one pack and across packs from the same session):

| Real value | Replace with |
|---|---|
| Device hostnames | `dev-01`, `dev-02`, … |
| Jump host names | `jump-01`, … |
| IPv4 addresses | Documentation ranges `192.0.2.x`, `198.51.100.x`, `203.0.113.x` |
| IPv6 addresses | `2001:db8::/32` |
| Domain names | `example.net` |
| Serial numbers, chassis IDs, MAC addresses | `SERIAL-01`, `02:00:00:00:00:01` (locally administered) |
| Site, customer, tenant, VRF or description text that names the organization | `site-a`, `tenant-a`, `vrf-a`, `desc-01` |
| AS numbers if they identify the organization | private range `64512–65534` |

Keep the **shape** of data intact: same key names, same types, same list lengths, same
path structure. Don't truncate values that are structurally significant (for example
interface names, version strings, model names and revisions are **not** sensitive and
must be kept as they are).

The pseudonym mapping is kept **only on the work device** in
`testing/.redaction-map.local.yaml`, which is git-ignored. Never commit or copy it.

## Getting results back

Preferred: commit `testing/results/` and `testing/devicedata/` on a branch named
`testing/<date>-<request>` and push it. If pushing isn't possible from the work
device, archive only those two folders and transfer the archive the approved way.

Before handing results over, the human tester reviews the diff. The test agent lists
everything it redacted, by kind and count, at the end of each report, so the review is
quick.
