# AI Agent Guide

Canopy is a vendor-agnostic, cross-platform gNMI and YANG browser written in Go.
Design: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). Read it before changing
anything structural.

## Two environments

Canopy is developed and tested in two places, and agents run in both:

| Environment | Has | Agent's job | Skill |
|---|---|---|---|
| **Development machine** | Code, no network devices | Build features, mocks from device data, analyze results | `canopy-triage` |
| **Work device** | Lab and production devices, limited AI access | Run tests with the human, collect sanitized device data, write reports | `canopy-test`, `canopy-collect` |

They exchange files only through [`testing/`](testing/README.md): requests go to the
work device, reports and device data packs come back. If you don't know which
environment you're in, ask the human.

## Skills

Skills live in `.agents/skills/<name>/SKILL.md`. If your agent doesn't load skills
automatically, read the file and follow it.

- `canopy-collect`: collect a sanitized device data pack (read-only gNMI).
- `canopy-test`: run a test session from a request or plan; write a report.
- `canopy-triage`: analyze returned reports and data; write fixes and new requests.

## Rules (quick reference)

- NEVER commit secrets, real hostnames, IPs, serials or organization names. Follow the
  sanitization rules in `testing/README.md`.
- NEVER change device configuration without the human's explicit approval for that
  specific action, and only on lab devices.
- NEVER edit files under `testing/results/` or `testing/devicedata/`; they're records.
- Keep everything buildable on macOS and Windows (primary) and Linux. No CGo.
