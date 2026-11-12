# Local development lab

One Nokia SR Linux node, so Canopy can be developed against a real gNMI device on the
development machine. Other platforms (NX-OS, IOS XR, EOS, Junos) need vendor images
and are covered by device data packs from the work device instead (see
`testing/README.md`).

Everything here is machine-independent: commands run **from the repository root**, and
generated files (lab CA, lab SSH key, downloaded models) stay in `lab/` and are
gitignored.

## Requirements

- Linux, or Windows with a WSL 2 distribution `Ubuntu` (Ubuntu 24.04).
- Docker Engine and Containerlab, with the user in the `docker` group.
- For the QA setup: Go, kind, kubectl and Tilt (network-operator's `make` installs
  kind and kustomize into its own `bin/`).

On Windows, prefix every `bash lab/operator/lab.sh …` command below with
`wsl -d Ubuntu --` (WSL starts in the current folder, so the relative path works). On
Linux, run it as is.

## Start and stop the SR Linux node

```bash
bash lab/operator/lab.sh node
```

```bash
bash lab/operator/lab.sh node-down
```

`node` deploys the node with a fresh config and exports its CA to `lab/.tls/ca.pem`.
The containerlab runtime folder is `~/clab` on the Linux filesystem (SR Linux can't set
file permissions on a Windows drive). WSL stops when idle, which stops the node; run
`node` again after a restart (this regenerates the CA).

## Connecting

| | |
|---|---|
| gNMI address | `127.0.0.1:57400` (on Windows, WSL forwards the port) |
| TLS | Yes, from the containerlab CA (`lab/.tls/ca.pem`, server name `srl1.canopy-dev.io`), or `--skip-verify` |
| Credentials | SR Linux lab defaults: `admin` / `NokiaSrl1!` |
| Encodings | `json_ietf` works for native paths without an origin |
| Version path | `/system/information/version` returns e.g. `v26.7.1-554-g78ed635f70a` |

YANG models matching the node (needed by the presets and the IDE configurations):

```bash
mkdir -p lab/models/srlinux-v26.7.1 && curl -sSL https://codeload.github.com/nokia/srlinux-yang-models/tar.gz/refs/tags/v26.7.1 | tar -xz -C lab/models/srlinux-v26.7.1 --strip-components=1
```

## Canopy presets and SSH jump host

[`connections.yaml`](connections.yaml) has the Canopy presets used for testing. The IDE
run configurations load it through `CANOPY_CONNECTIONS`; from a shell, set
`CANOPY_CONNECTIONS=lab/connections.yaml`. Paths in it are relative to `lab/`.

| Preset | Path to the node |
|---|---|
| `lab-srl1` | `127.0.0.1:57400`, TLS without verification |
| `lab-srl1-verified` | `127.0.0.1:57400`, TLS verified with `lab/.tls/ca.pem` (server name `srl1.canopy-dev.io`) |
| `lab-srl1-jump` | `172.20.20.2:57400` (the lab network, not reachable from Windows) through the SSH jump host, TLS verified |

The jump host is an SSH server on `127.0.0.1:2222` (key authentication only) with a
user `canopy` that may only forward ports. Set it up once, as root:

```bash
wsl -d Ubuntu -u root -- bash lab/operator/lab.sh jumphost
```

It creates the lab key `lab/.ssh/canopy_lab_ed25519` if missing and authorizes it. The
first connection asks to trust the jump host's key (in the UI, or with
`--accept-host-key <fingerprint> --save-host-key`); `jumphost` prints the fingerprint.

## network-operator against the lab (QA setup)

`lab/operator/lab.sh` runs network-operator in a kind cluster with Tilt and connects it
to `srl1`, so a change in the operator can be checked on a real device.

```text
 Host                        Linux / WSL (Docker)
 Canopy ──127.0.0.1:57400──▶ srl1 (clab network 172.20.20.0/24) ◀── network-operator
 browser ─localhost:10350──▶ Tilt                                    (kind node
                                                                      routes to the clab network)
```

```bash
bash lab/operator/lab.sh up
```

```bash
bash lab/operator/lab.sh tilt
```

```bash
bash lab/operator/lab.sh device
```

- `up` deploys the node, clones network-operator to `~/network-operator` on the Linux
  filesystem, creates the kind cluster `network-operator` and checks that it can reach
  the lab network (routed; the node is not attached to the `clab` network, which breaks
  the node address after restarts). The clone comes from a `network-operator` checkout
  next to this repository if there is one (same commit and branches), else from GitHub;
  override with `OPERATOR_SRC` and `OPERATOR_DIR`.
- `tilt` must keep running (its own terminal). Tilt UI: http://localhost:10350.
- `device` creates Device **`leaf1`** (`lab/operator/leaf1-device.yaml`) pointing at the
  SR Linux node, with the lab's CA so the operator verifies TLS properly. It should
  become `Ready`.
- All of the operator's **Tilt samples target `leaf1`**, so the sample buttons in the
  Tilt UI configure the SR Linux node. Samples for kinds the SR Linux provider doesn't
  support (many are NX-OS- or OpenConfig-specific) will fail to reconcile, which is
  expected.
- **Don't trigger Tilt's own `leaf1` resource.** It would replace the Device with the
  sample (address `192.168.5.2:9339`, OpenConfig provider, no TLS). If it happens, run
  `lab.sh device` again.
- `status` and `down` do what they say.

Why the operator runs from a clone on the Linux filesystem: a Windows checkout has
Windows line endings (the operator's shell scripts fail in Linux), and Tilt's file
watching doesn't work on Windows drives. Edit that clone for live reload (on Windows
through `\\wsl.localhost\Ubuntu\home\<user>\network-operator`), or pull changes into it
from your main checkout.

Docker in WSL is configured with the classic image store
(`/etc/docker/daemon.json`: `{"features": {"containerd-snapshotter": false}}`), because
`kind load` fails with Docker 29's default containerd image store.

Verified on 2026-11-12: Device `leaf1` Ready (Nokia 7220 IXR-D2L, v26.7.1), and Tilt's
`banner` sample produced `/system/banner/login-banner` on the device.

## Later: tunnel setup like the team uses (not done yet)

The team works the way the Tiltfile was designed: Tilt's `leaf1` sample gets the
address `host.docker.internal:9339`, and a tunnel or port forward on port 9339
points at whatever should be tested (the fake gNMI server, a lab switch, a device
behind a jump host). Retargeting is just restarting the tunnel.

What this lab needs for that (checked 2026-11-12):

| # | Piece | Status |
|---|---|---|
| 1 | `host.docker.internal` must resolve. The Tiltfile resolves it with `nslookup` in a busybox container; in WSL it returns NXDOMAIN (WSL forwards DNS to Windows). Fix either with a small DNS forwarder in WSL that answers this name (for example with `172.17.0.1`) and is set as Docker's DNS (preferred, lab-only), or with a Windows `hosts` entry (needs admin) | To do |
| 2 | A forwarder on 9339, for example `lab.sh tunnel [target]`, defaulting to SR Linux plaintext gNMI `172.20.20.2:57401` | To do |
| 3 | Plaintext gNMI on the target, because the sample Device has no TLS. SR Linux in containerlab already serves it on **57401** (verified with a Get) | Works |
| 4 | Credentials: the sample secret is `admin`/`admin`, SR Linux uses `admin`/`NokiaSrl1!`. Add a matching user through the lab startup config if SR Linux accepts the password, otherwise override the sample secret for the lab | To do |
| 5 | Start Tilt with `PROVIDER=srlinux.nokia.networking.metal.ironcore.dev`; the Tiltfile already applies it to the sample Device | To do |

Once done, Tilt's own `leaf1` resource works as designed and
`lab/operator/leaf1-device.yaml` is no longer needed. Canopy's recording proxy
(`docs/ARCHITECTURE.md`, 5.9) should be able to sit on the same 9339 endpoint, so it
fits into this workflow unchanged.

## Quick check with the gNMIc container

```bash
docker run --rm --network host ghcr.io/openconfig/gnmic:latest -a 127.0.0.1:57400 -u admin -p "NokiaSrl1!" --skip-verify capabilities
```
