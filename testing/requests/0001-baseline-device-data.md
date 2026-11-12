# 0001 — Baseline device data packs

- **Type:** collect
- **Commit/branch to test:** n/a (Canopy isn't built yet; uses gNMIc)
- **Plan cases:** none
- **Devices:** one device per platform you can reach, lab preferred:
  Cisco NX-OS, Cisco IOS XR, Nokia SR Linux, Arista EOS, Juniper Junos, and any
  OpenConfig-only device. Several NX-OS versions are welcome (version detection depends
  on them).
- **Writes needed:** no

## Why

The development machine has no devices. These packs become the mocks, fixtures and
platform profiles that Canopy is developed and unit-tested against. They also answer
open questions in `docs/ARCHITECTURE.md` section 11, especially version detection for
platforms other than NX-OS.

## What changed since the last run

First run.

## Instructions

Use the `canopy-collect` skill for each device. In addition to its defaults:

- **NX-OS:** also collect `get --type state --path /System/showversion-items` if
  the device accepts it, and note the `Cisco-NX-OS-device` revision.
- **SR Linux:** try `/system/information` with and without `origin` and note which
  works and which encoding.
- **IOS XR:** note whether the version is in an OpenConfig path, a native
  `Cisco-IOS-XR-*` path, or only in Capabilities.
- **Arista EOS:** note the exact version string format (for example `4.31.0F`).
- **All:** if the device also has NETCONF enabled, only *note* that (port and whether
  `ietf-netconf-monitoring` is in Capabilities). Don't use NETCONF yet.

## Evidence needed

- One data pack per device, complete per the skill's file list.
- One report in `testing/results/` listing the packs and anything that didn't work.
