# Canopy

A vendor-agnostic gNMI and YANG browser. Point it at a device (directly or through SSH
jump hosts), load the YANG models, walk the schema tree, read live data, copy parts of
it as JSON or YAML, and generate the matching gNMIc commands.

One Go binary, with a CLI and a local web UI. macOS and Windows are the primary
platforms; Linux works too. No CGo.

Design and roadmap: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). Status: milestone
M1 (connect and browse).

## Build

Requires Go 1.27 or newer.

```bash
go build -o bin/canopy ./cmd/canopy
```

On Windows use `bin/canopy.exe`. The web UI is prebuilt into `internal/web/dist`, so the
Go build needs no Node.js. To change the UI (Node.js 22 or newer):

```bash
cd web && npm ci && npm run build
```

Run the tests (they need no device; a fake gNMI server and SSH server run in-process):

```bash
go test ./...
```

## Use

Credentials come from `GNMI_USERNAME` and `GNMI_PASSWORD`, or a prompt in a terminal.
They are never written to logs or generated commands.

```bash
canopy connect -a 10.0.0.5:57400 --skip-verify
```

```bash
canopy get -a 10.0.0.5:57400 --skip-verify -p /system/information -f yaml
```

```bash
canopy get -a 10.0.0.5:9339 --tls-ca ca.pem -J admin@jump.example.net -p '/interfaces/interface[name=*]/state' -f flat
```

```bash
canopy tree --yang ./models --find admin-state
```

```bash
canopy generate gnmic -a 10.0.0.5:57400 --skip-verify -p /system --mode stream
```

```bash
canopy ui --yang ./models
```

`canopy ui` opens the browser at `http://127.0.0.1:<port>/?token=…`; it only listens on
the loopback interface and refuses requests without the token.

For scripts and agents, every command takes `--output json` (stable envelope with
`schemaVersion`, `ok`, `result` or `error`), `--non-interactive`, and
`--log-level debug --log-file canopy.log`. Exit codes: `0` success, `1` usage,
`2` connection or authentication, `3` device error (gRPC code in the JSON), `4` model
error, `5` internal.

TLS options: `--insecure` (plaintext), `--skip-verify`, `--tls-ca`, `--tls-cert` with
`--tls-key`, `--tls-server-name`. SSH jump hosts (`-J`, repeat or comma-separate for a
chain) use the SSH agent (Windows OpenSSH agent pipe, or `SSH_AUTH_SOCK`), keys from
`--ssh-identity` and `~/.ssh`, and `~/.ssh/config` (`HostName`, `User`, `Port`,
`IdentityFile`, `ProxyJump`). Unknown host keys are refused unless accepted
(`--accept-host-key SHA256:…`, or confirmed at the prompt or in the UI). Key passphrases
come from `CANOPY_SSH_PASSPHRASE` or a prompt.

### Saved connections

Save a device once and use it by name. Several devices can be connected at the same
time in the UI (the chips under the connection form switch between them; each keeps its
own paths, data and YANG folders).

```bash
canopy conn save leaf1 -a 10.0.0.5:57400 --tls-ca ca.pem -u admin -J admin@jump.example.net --yang ./models/nxos-10.4-3 -p /System/intf-items --password-env LEAF1_PASSWORD
```

```bash
canopy get --conn leaf1
```

```bash
canopy conn list
```

Flags given with `--conn` override the saved values. `get` and `generate gnmic` use
the saved paths when no `--path` is given. A preset stores target, TLS, user name,
jump hosts and key files, preferred encoding, YANG folders, favourite paths, a
description and tags, plus what the device reported the last time
(`lastSeen`). **Passwords are never stored**; `--password-env` names the environment
variable with that device's password (default `GNMI_PASSWORD`).

Presets live in `canopy conn path` (the user config folder, or `$CANOPY_CONFIG_DIR`).
Extra read-only preset files, for example a team file, are added with
`CANOPY_CONNECTIONS` (separated like `PATH`); relative paths in them resolve against the
file's folder. [`lab/connections.yaml`](lab/connections.yaml) holds the lab presets.

**Git Bash on Windows** rewrites arguments that start with `/` into Windows paths
(`/system` becomes `C:/Program Files/Git/system`). Run with `MSYS_NO_PATHCONV=1`, or
use PowerShell.

## IntelliJ IDEA / GoLand

Shared run configurations are in [`.run/`](.run) and show up in the run menu
automatically:

| Configuration | What it does |
|---|---|
| Build canopy | Builds `bin/canopy` and prints the version |
| Canopy UI | Starts the UI; the lab presets are in the "Saved" list |
| Canopy UI (rebuild web) | Same, after rebuilding the frontend (needs Node.js and `npm ci` in `web/`) |
| All tests | `go test ./...` |
| Web build / watch / check | Frontend build, rebuild on change, type check |
| Lab - list connections | Lists the lab presets (and your own) |
| Lab - connect lab-srl1 / -verified / -jump | Connects with each lab preset: TLS skip-verify, TLS verified with the lab CA, through the SSH jump host |
| Lab - get lab-srl1 | Reads the preset's favourite paths |
| Lab - tree find login-banner | Schema search in the SR Linux models |

The UI and Lab configurations set `CANOPY_CONNECTIONS` to
[`lab/connections.yaml`](lab/connections.yaml), which is exactly what is used for local
testing, and `GNMI_PASSWORD` to the SR Linux lab default.

The IDE needs the Go plugin (built into GoLand); the `Web` configurations need the
JavaScript plugin (IntelliJ IDEA Ultimate, GoLand, WebStorm).

## Local lab

[`lab/README.md`](lab/README.md) describes a Nokia SR Linux node in containerlab (WSL on
Windows) and network-operator in kind, used for development and QA. The `Canopy UI` and
`Lab` configurations expect the SR Linux models matching the lab node in
`lab/models` (not committed):

```bash
mkdir -p lab/models/srlinux-v26.7.1 && curl -sSL https://codeload.github.com/nokia/srlinux-yang-models/tar.gz/refs/tags/v26.7.1 | tar -xz -C lab/models/srlinux-v26.7.1 --strip-components=1
```

## Testing on other devices

Development and device testing happen on different machines; agents on both sides
follow the skills in `.agents/skills`. See [`AGENTS.md`](AGENTS.md) and
[`testing/README.md`](testing/README.md).

## License

Apache-2.0, see [`LICENSE`](LICENSE).
