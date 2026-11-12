# Local development lab

One Nokia SR Linux node, so Canopy can be developed against a real gNMI device on the
development machine. Other platforms (NX-OS, IOS XR, EOS, Junos) need vendor images
and are covered by device data packs from the work device instead (see
`testing/README.md`).

## Requirements

WSL 2 distribution `Ubuntu` (Ubuntu 24.04) with Docker Engine and Containerlab installed, and
the user in the `docker` group.

## Start and stop

From Windows (the lab's runtime folder must be on the Linux filesystem, because SR Linux
can't set file permissions on a Windows drive):

```bash
wsl -d Ubuntu -u root --cd /mnt/w/Projects/Go/Canopy/lab -- env CLAB_LABDIR_BASE=/home/nagato/clab containerlab deploy -t canopy-dev.clab.yml
```

```bash
wsl -d Ubuntu -u root --cd /mnt/w/Projects/Go/Canopy/lab -- env CLAB_LABDIR_BASE=/home/nagato/clab containerlab destroy -t canopy-dev.clab.yml --cleanup
```

WSL stops when idle, which stops the node. Deploy again after a restart (add
`--reconfigure` to start from a clean config).

## Connecting

| | |
|---|---|
| gNMI address | `127.0.0.1:57400` from Windows (WSL forwards the port) |
| TLS | Yes, self-signed (`--skip-verify`, server name `srl1.canopy-dev.io`) |
| Credentials | SR Linux lab defaults: `admin` / `NokiaSrl1!` |
| Encodings | `json_ietf` works for native paths without an origin |
| Version path | `/system/information/version` returns e.g. `v26.7.1-554-g78ed635f70a` |

## network-operator against the lab (QA setup)

`lab/operator/lab.sh` runs network-operator in a kind cluster with Tilt and connects it
to `srl1`, so a change in the operator can be checked on a real device.

```text
 Windows                     WSL (Ubuntu, Docker)
 Canopy ──127.0.0.1:57400──▶ srl1 (clab network 172.20.20.0/24) ◀── network-operator
 browser ─localhost:10350──▶ Tilt                                    (kind node
                                                                      routes to the clab network)
```

```bash
wsl -d Ubuntu -- bash /mnt/w/Projects/Go/Canopy/lab/operator/lab.sh up
```

```bash
wsl -d Ubuntu -- bash /mnt/w/Projects/Go/Canopy/lab/operator/lab.sh tilt
```

```bash
wsl -d Ubuntu -- bash /mnt/w/Projects/Go/Canopy/lab/operator/lab.sh device
```

- `up` deploys SR Linux, clones network-operator to `~/network-operator` in WSL (from
  the `W:` clone, so the same commit and branches), creates the kind cluster
  `network-operator` and checks that it can reach the lab network (routed; the node is
  not attached to the `clab` network, which breaks the node address after restarts).
- `tilt` must keep running (its own terminal). Tilt UI: http://localhost:10350.
- `device` creates Device **`leaf1`** (`lab/operator/leaf1-device.yaml`) pointing at the
  SR Linux node, with the lab's CA so the operator verifies TLS properly. It should
  become `Ready`.
- All of the operator's **Tilt samples target `leaf1`**, so the sample buttons in the
  Tilt UI now configure the SR Linux node. Samples for kinds the SR Linux provider
  doesn't support (many are NX-OS- or OpenConfig-specific) will fail to reconcile,
  which is expected.
- **Don't trigger Tilt's own `leaf1` resource.** It would replace the Device with the
  sample (address `192.168.5.2:9339`, OpenConfig provider, no TLS). If it happens, run
  `lab.sh device` again.
- `status` and `down` do what they say.

Why the operator runs from a WSL clone: the `W:` checkout has Windows line endings
(the operator's shell scripts fail in Linux), and Tilt's file watching doesn't work on
Windows drives. Edit the WSL clone (for example via `\\wsl$\Ubuntu\home\nagato\network-operator`
in the IDE) for live reload, or pull changes into it from the `W:` clone.

Docker in WSL is configured with the classic image store
(`/etc/docker/daemon.json`: `containerd-snapshotter: false`), because `kind load`
fails with Docker 29's default containerd image store.

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
| 2 | A forwarder on 9339 in WSL, for example `lab.sh tunnel [target]`, defaulting to SR Linux plaintext gNMI `172.20.20.2:57401` | To do |
| 3 | Plaintext gNMI on the target, because the sample Device has no TLS. SR Linux in containerlab already serves it on **57401** (verified with a Get) | Works |
| 4 | Credentials: the sample secret is `admin`/`admin`, SR Linux uses `admin`/`NokiaSrl1!`. Add a matching user through the lab startup config if SR Linux accepts the password, otherwise override the sample secret for the lab | To do |
| 5 | Start Tilt with `PROVIDER=srlinux.nokia.networking.metal.ironcore.dev`; the Tiltfile already applies it to the sample Device | To do |

Once done, Tilt's own `leaf1` resource works as designed and
`lab/operator/leaf1-device.yaml` is no longer needed. Canopy's recording proxy
(`docs/ARCHITECTURE.md`, 5.9) should be able to sit on the same 9339 endpoint, so it
fits into this workflow unchanged.

## Quick check from WSL with the gNMIc container

```bash
wsl -d Ubuntu -- docker run --rm --network host ghcr.io/openconfig/gnmic:latest -a 127.0.0.1:57400 -u admin -p "NokiaSrl1!" --skip-verify capabilities
```
