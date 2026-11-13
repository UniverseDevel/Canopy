// Inline help: a small "?" next to a part of the UI opens a popover that says
// what it is, how to use it, and how it works with the other parts. All text
// lives here, keyed by topic, so it stays consistent; openHelpIndex shows every
// topic on one page.
//
// Text format: a string is a paragraph, an array of strings is a list, and
// `backticks` mark code.

import { append, clear, h, modal } from "./dom";

export type Target = "tab:Data" | "tab:Snapshots" | "tab:Resources" | "tab:Fixtures" | "left:Schema" | "left:Capabilities" | "console";

export type TopicId = "devices" | "connect" | "tls" | "jump" | "keychain" | "cluster" | "models" | "catalog" | "cache" | "netconf" | "tree" | "qualified" | "markedOnly" | "capabilities" | "read" | "dataView" | "command" | "configure" | "snapshots" | "operator" | "samples" | "verify" | "fixtures" | "fixtureRun" | "shadow" | "fixtureBuild" | "console";

interface Topic {
  title: string;
  group: string;
  body: (string | string[])[];
  related?: TopicId[];
  go?: { label: string; target: Target };
  cli?: string;
}

const T = {
  devices: {
    title: "Devices", group: "Devices",
    body: [
      "Canopy has two workspaces, switched at the top left: Devices (this one: plain gNMI) and network-operator (a cluster's Devices and resources).",
      "The devices you can connect to: the ones saved in your connections file, plus any connected without saving (tagged \"not saved\", gone when Canopy restarts), and cluster Devices while they are connected (tagged \"cluster\", from Browse in Devices). Several can be connected at once; click a connected device to switch to it. Each keeps its own paths, data and models.",
      "Connections live in Canopy's server, not in the page, so reloading the page keeps them.",
      "Pencil edits a device: change it, rename it or delete it. Every change is written to the connections file it came from.",
    ],
    related: ["connect", "cluster", "models"],
    cli: "canopy conn list",
  },
  connect: {
    title: "Connecting a device", group: "Devices",
    body: [
      "Target is the device's gNMI address, `host:port` (often 57400 or 9339). The user name and password are sent to the device; the password is never written to a file.",
      "Tick Save to keep the device as a preset (in Canopy's config folder) and use it by name, here and in the CLI. Without a typed password, Canopy uses the environment variable named in the preset (default `GNMI_PASSWORD`).",
      "On connect Canopy reads the device's capabilities, detects its platform and version, and loads the matching YANG models.",
    ],
    related: ["tls", "jump", "keychain", "models"],
    cli: "canopy conn save NAME -a host:57400 ...",
  },
  tls: {
    title: "Security (TLS)", group: "Devices",
    body: [[
      "Verified TLS: checks the device certificate against the CA file. Use TLS server name when the certificate's name differs from the address.",
      "Skip verify: encrypted, but any certificate is accepted. Fine in a lab, not on a network you don't control.",
      "Insecure: plaintext gRPC, for devices that have TLS off.",
    ]],
    related: ["connect", "command"],
  },
  jump: {
    title: "SSH jump hosts", group: "Devices",
    body: [
      "When the device is only reachable from another machine, Canopy goes through it with SSH, like `ssh -J`. Give `[user@]host[:port]` or a `~/.ssh/config` alias; several, comma-separated, form a chain.",
      "Keys come from the SSH agent, the SSH key file field, and `~/.ssh`. An unknown host key is shown for you to accept once.",
      "The generated gNMIc commands then run gNMIc on the last jump host through ssh, so gNMIc must be installed there.",
    ],
    related: ["connect", "command", "cluster"],
  },
  keychain: {
    title: "Remembering passwords", group: "Devices",
    body: [
      "Opt-in, per saved device. A password is never written into the connections file itself; it can be kept in one of two places:",
      [
        "OS keychain: macOS Keychain, Windows Credential Manager or Secret Service on Linux.",
        "Encrypted file: next to the device's connections file (`connections.yaml` → `connections.secrets.yaml`), encrypted with AES-256-GCM. The key is in this computer's OS keychain, so the file is useless elsewhere. Canopy makes sure git ignores it (it adds `*.secrets.yaml` to a .gitignore when needed).",
      ],
      "When a password is asked for while connecting, you can choose to remember it once it works. Storing it in one place removes it from the other; Forget removes it.",
    ],
    related: ["connect"],
    cli: "canopy conn password NAME --store file",
  },
  cluster: {
    title: "Cluster (network-operator workspace)", group: "Devices",
    body: [
      "The network-operator workspace lists network-operator's Device resources from a Kubernetes cluster. Select one to see the resources that configure it (Resources tab); that needs only the cluster.",
      "Verify and Browse in Devices connect to the Device the way the operator does: the address, credentials and CA from the Device's Secrets, so you see exactly what the operator talks to. Browse in Devices opens that connection in the Devices workspace (schema, data, snapshots).",
      "⚙ chooses the kubeconfig, context, and SSH jump hosts for device addresses that are only reachable from the cluster network. Canopy keeps them in its config folder (operator.json).",
    ],
    related: ["operator", "fixtures", "jump"],
    go: { label: "Open network-operator", target: "tab:Resources" },
    cli: "canopy operator devices",
  },
  models: {
    title: "YANG models", group: "Schema",
    body: [
      "The schema tree comes from YANG models. When a device connects, Canopy recognizes its platform from Capabilities (platform profiles for SR Linux, SR OS, NX-OS, IOS XR, IOS XE, Junos, Huawei, EOS and OpenConfig), reads its software version where the profile says how, and downloads only the matching release from GitHub into its cache.",
      "Pick another source or version here, or load a local folder of `.yang` files instead (for offline use or your own models). The list of versions is kept for a day; ↻ checks GitHub for new ones now (and picks again for a device that got a nearby release).",
      "A node's details show its YANG source; Open on GitHub opens the same file and line in the repository, at the loaded release.",
      "Modules whose imports can't be found are skipped and listed as warnings.",
    ],
    related: ["catalog", "netconf", "cache", "tree", "capabilities"],
    cli: "canopy models versions srlinux",
  },
  netconf: {
    title: "Models from the device", group: "Schema",
    body: [
      "Downloads the YANG modules the device itself implements, for devices whose models aren't published or don't match a release. Canopy opens NETCONF over SSH (port 830 unless you change it) with the same user name, password and jump hosts, lists the schemas the device offers (ietf-netconf-monitoring) and downloads each with get-schema (RFC 6022).",
      "It only reads. NETCONF has to be enabled on the device. The modules are kept per device in the model cache and reused next time; they are also added to the catalog.",
    ],
    related: ["models", "catalog", "jump"],
    cli: "canopy models from-device --conn NAME",
  },
  cache: {
    title: "Downloaded models", group: "Schema",
    body: [
      "Every model set Canopy downloads stays in its cache, so it is downloaded once. Downloaded lists the sets with their size; Remove deletes one to free space (it is downloaded again when needed, and stays searchable in the catalog).",
      "Share identical files stores each file that several sets contain (IETF and OpenConfig modules, unchanged vendor modules) once, as hard links. Canopy never edits cached files, so nothing else changes. Run it again after downloading more.",
    ],
    related: ["models", "catalog"],
    cli: "canopy models cache",
  },
  catalog: {
    title: "Model catalog", group: "Schema",
    body: [
      "A local index of the modules in model releases, searchable by module name, prefix, namespace and description. Every word must match the start of a word: `openconfig interf` finds openconfig-interfaces.",
      "Downloaded releases are indexed from their files (revision, OpenConfig version, imports, deviations). Other releases can be indexed from a listing of their file names on GitHub, without downloading, so you can see which releases contain a module before choosing one.",
      "Click a module to list the releases that have it, with the revision in each, and load one of them.",
    ],
    related: ["models", "capabilities"],
    cli: "canopy models search WORDS",
  },
  tree: {
    title: "Schema tree", group: "Schema",
    body: [
      "Every node the models define, whether or not the device has data for it. Search finds nodes by name or path.",
      "Tick a node to mark its path: marked paths are what the Data tab reads, what snapshots save, and what the command preview uses. Click a node to see its details in the middle: type, description, its path, Get this path, list Instances on the device, and Configure… for config nodes.",
    ],
    related: ["qualified", "markedOnly", "read", "configure"],
    cli: "canopy tree --find NAME",
  },
  qualified: {
    title: "Qualified paths", group: "Schema",
    body: [
      "Several modules can define nodes with the same name, such as `system` from IETF, OpenConfig and the vendor. Without module names they look like duplicates.",
      "Qualified paths name the module where it changes, e.g. `/srl_nokia-system:system/name`, and OpenConfig paths get the `openconfig:` origin the device needs. Devices accept these paths as they are.",
      "Untick for plain paths, as most documentation writes them.",
    ],
    related: ["tree", "read"],
  },
  markedOnly: {
    title: "Marked only", group: "Schema",
    body: ["Narrows the tree to the marked paths, their parents, and what is under them, so you see your selection in a large model."],
    related: ["tree"],
  },
  capabilities: {
    title: "Capabilities", group: "Schema",
    body: [
      "What the device reports about itself: the gNMI version, the encodings it supports, and every YANG module with its revision. It belongs to the whole device, not to one path.",
      "Canopy uses it to detect the platform and models, and to pick the default encoding.",
    ],
    related: ["models", "read"],
    go: { label: "Show capabilities", target: "left:Capabilities" },
    cli: "canopy connect -a host:57400",
  },
  read: {
    title: "Reading data (Get)", group: "Data",
    body: [
      "Reads the paths listed here from the device with a gNMI Get. Mark nodes in the tree to add their paths, or type them; list keys can be filled in, e.g. `interface[name=ethernet-1/1]`, or `*` for all.",
      [
        "Type: all, config, state, or operational data.",
        "Encoding: how the device sends values; auto picks JSON_IETF or JSON from capabilities.",
      ],
      "Paths with different origins (OpenConfig and native) are sent as one Get per origin and merged, since devices refuse to mix them.",
    ],
    related: ["dataView", "command", "snapshots", "tree"],
    cli: "canopy get -a host:57400 -p PATH -f yaml",
  },
  dataView: {
    title: "Viewing and copying data", group: "Data",
    body: [
      "Tree, JSON, YAML or flat (one `path = value` per line). Copy JSON and YAML copy the ticked parts of the data, or everything when nothing is ticked, ready for a config file or a Configure… value. ⊞ and ⊟ expand and collapse the tree.",
    ],
    related: ["read", "configure"],
  },
  command: {
    title: "Command preview", group: "Data",
    body: [
      "The gNMIc command that does the same read as the Data tab, on one line, for bash/zsh or PowerShell. It follows what you mark and choose, so you can copy it into a script or run it where Canopy isn't available.",
      [
        "Get: a single read.",
        "Subscribe once: like Get, through a subscription.",
        "Subscribe stream: keeps sending updates, every sample interval or on change.",
        "Subscribe poll: sends data when you ask for it.",
      ],
      "Credentials are never in the command: it uses `$GNMI_USERNAME` and `$GNMI_PASSWORD`, which your shell fills in. With jump hosts it runs gNMIc on the last one through ssh.",
    ],
    related: ["console", "jump", "configure"],
    cli: "canopy generate gnmic --conn NAME --mode stream",
  },
  configure: {
    title: "Changing configuration", group: "Data",
    body: [
      "Configure… on a config node builds a gNMI Set: update (merge), replace (overwrite the subtree), or delete. Start from the device's current value or from the schema.",
      "Every edit is checked against the loaded models and shown as the SetRequest and the matching `gnmic set` command before anything is sent. Applying needs the confirmation tick, and afterwards shows what changed.",
      "Only change lab devices, with the owner's approval.",
    ],
    related: ["snapshots", "command", "console"],
  },
  snapshots: {
    title: "Snapshots", group: "Compare and test",
    body: [
      "A snapshot saves a read of the marked paths, with the device and time, in Canopy's config folder. Compare two snapshots, or a snapshot with the device now, to see exactly what a change did.",
      "Ignore leaves out paths that change on their own (counters, uptime).",
      "Snapshots are also the building blocks of fixtures: before, after applying a resource, and after deleting it.",
    ],
    related: ["read", "configure", "fixtureBuild"],
    go: { label: "Open Snapshots", target: "tab:Snapshots" },
    cli: "canopy snapshot take --conn NAME -p PATH",
  },
  operator: {
    title: "Operator", group: "Compare and test",
    body: [
      "The Resources tab of the network-operator workspace: the resources (Banner, Interface, …) that point at the Device selected in the cluster list, with their Ready status.",
      [
        "Pause stops the operator from changing the device or a resource, so you can test by hand; Resume hands it back.",
        "Verify checks that what the resource asks for is really on the device.",
        "Fixture starts a test fixture from the resource.",
      ],
    ],
    related: ["verify", "cluster", "fixtures"],
    go: { label: "Open Resources", target: "tab:Resources" },
  },
  samples: {
    title: "Samples", group: "Compare and test",
    body: [
      "network-operator's example resources from `config/samples` of the checkout chosen in the Fixtures tab: the common ones and those in the provider's own folder (e.g. `nokia/srlinux`). It is what Tilt's sample buttons apply, but aimed at the selected Device instead of `leaf1`.",
      "Apply… shows the YAML to review or edit, then sets the namespace, `spec.deviceRef` and the device-name label to this Device and applies it with server-side apply (Canopy as field manager), with the Secrets the sample uses. Delete removes it again. The operator then changes or removes the configuration on the device, so use it on lab devices.",
      "In the cluster: applied means an object of that kind and name is in the Device's namespace; \"used by\" means it points at another Device (sample names are fixed, so two Devices in one namespace share them).",
      "Every apply and delete is in the Console with the equivalent kubectl command.",
    ],
    related: ["operator", "verify", "fixtures"],
    go: { label: "Open Resources", target: "tab:Resources" },
  },
  verify: {
    title: "Verify", group: "Compare and test",
    body: [
      "Takes the provider's test fixture for the resource's kind, finds which device leaves the operator writes for it, fills in this resource's values, and reads those leaves from the device. Each leaf is ok, mismatch, or missing.",
      "Verify a deleted resource checks that its configuration is gone.",
    ],
    related: ["operator", "fixtures"],
    cli: "canopy operator verify KIND NAME",
  },
  fixtures: {
    title: "What is a fixture", group: "Compare and test",
    body: [
      "A test case for network-operator, kept in its repository under `test/gnmi/testdata/<provider>/` as a `.txtar` file (several files in one). It holds:",
      [
        "the resource YAML, e.g. a Banner;",
        "`state/preload`: the device configuration before the test (optional);",
        "`state/expect`: what it must be once the operator applied the resource;",
        "`state/delete`: what it must be after the resource is deleted.",
      ],
      "If both comparisons match, the operator translates that resource correctly for that vendor.",
      "Which checkout: choose it at the top of the Fixtures tab (Browse…, or one used before), for example your fork with work that isn't public yet. It applies to fixtures and Verify for every device, and Canopy remembers it. Default: `--operator-repo`, `CANOPY_OPERATOR_REPO` or a `network-operator` folder next to Canopy's.",
    ],
    related: ["fixtureBuild", "fixtureRun", "snapshots"],
    go: { label: "Open Fixtures", target: "tab:Fixtures" },
    cli: "canopy fixture export ...",
  },
  fixtureRun: {
    title: "Running a fixture", group: "Compare and test",
    body: [
      "Runs a fixture against the operator in the cluster, as its own gNMI test does, with a shadow target in place of a device:",
      [
        "load `state/preload` into the shadow target;",
        "create a namespace, a Device pointing at the shadow target, and the resources;",
        "wait until they are Ready, then compare with `state/expect`;",
        "delete them, compare with `state/delete`, remove the namespace.",
      ],
      "No real device is touched. Keep the namespace to look at it afterwards.",
    ],
    related: ["shadow", "fixtures", "fixtureBuild"],
  },
  shadow: {
    title: "Shadow target", group: "Compare and test",
    body: [
      "A fake gNMI device that stores whatever is set on it and returns it on Get. The operator in the cluster must reach it at the address you give.",
      [
        "Embedded: Canopy runs it on the Listen address. Pods must be able to reach this machine.",
        "Remote: your own `canopy shadow --tls --http ADDR` somewhere the cluster can reach (e.g. on the kind network gateway); Canopy controls it through the Control URL.",
      ],
    ],
    related: ["fixtureRun"],
    cli: "canopy shadow --tls --http :57580",
  },
  fixtureBuild: {
    title: "Building a fixture", group: "Compare and test",
    body: [
      "On a lab device: mark the paths the resource touches, take a snapshot, apply the resource, take another when it is Ready, delete it, and take a third. Pick the three snapshots here with the resource's YAML (the Operator tab's Fixture button fills it in).",
      "Build creates the `.txtar`. Save writes it into the network-operator checkout; Run this fixture tests it straight away.",
    ],
    related: ["snapshots", "fixtures", "fixtureRun", "operator"],
  },
  console: {
    title: "Console", group: "Other",
    body: [
      "An audit log of everything the UI did with a device or cluster: connects, reads, changes, snapshots, pause/resume, fixture runs. Each entry has its result, its timing and the equivalent gNMIc or kubectl command; click a command to copy it.",
      "It is open at the bottom by default; drag its top edge to resize it, or close it with ✕ (Canopy remembers both). When something fails, such as a connect, the console opens and the failed entry flashes, with the full error.",
      "The server keeps the log, so it survives a page reload, and appends it to `audit.jsonl` in Canopy's config folder.",
    ],
    related: ["command"],
    go: { label: "Open the console", target: "console" },
  },
} satisfies Record<TopicId, Topic>;

const topics: Record<TopicId, Topic> = T;

let nav: (t: Target) => void = () => {};
// setHelpNav lets "Open …" links switch tabs.
export function setHelpNav(fn: (t: Target) => void) { nav = fn; }

function text(s: string): Node[] {
  return s.split("`").map((part, i) => (i % 2 ? h("code", {}, part) : document.createTextNode(part)));
}

function renderBody(t: Topic, onRelated: (id: TopicId) => void): HTMLElement {
  const el = h("div", { class: "help-body" });
  for (const b of t.body) {
    if (Array.isArray(b)) el.append(h("ul", {}, ...b.map((li) => { const x = h("li"); x.append(...text(li)); return x; })));
    else { const p = h("p"); p.append(...text(b)); el.append(p); }
  }
  if (t.cli) el.append(h("p", { class: "help-cli" }, h("span", { class: "muted" }, "CLI "), h("code", {}, t.cli)));
  const links: Node[] = [];
  if (t.go) links.push(h("button", { class: "link help-go", onclick: () => { close(); nav(t.go!.target); } }, t.go.label + " →"));
  if (t.related?.length) {
    links.push(h("span", { class: "muted" }, "See also "),
      ...t.related.flatMap((id, i) => [i ? document.createTextNode(" · ") : null,
        h("button", { class: "link", onclick: () => onRelated(id) }, topics[id].title)].filter(Boolean) as Node[]));
  }
  if (links.length) el.append(h("div", { class: "help-links" }, ...links));
  return el;
}

// ---------- popover ----------

let pop: HTMLElement | null = null;
let anchor: HTMLElement | null = null;

function close() {
  pop?.remove();
  pop = null;
  anchor = null;
}

function place() {
  if (!pop || !anchor) return;
  const r = anchor.getBoundingClientRect();
  const w = pop.offsetWidth, ht = pop.offsetHeight;
  const left = Math.max(8, Math.min(r.left - 12, innerWidth - w - 8));
  let top = r.bottom + 6;
  if (top + ht > innerHeight - 8) top = Math.max(8, r.top - ht - 6);
  pop.style.left = left + "px";
  pop.style.top = top + "px";
}

function show(id: TopicId, back: TopicId[]) {
  if (!pop) return;
  const t = topics[id];
  clear(pop);
  append(pop,
    h("div", { class: "help-head" },
      back.length ? h("button", { class: "link help-back", title: "Back", onclick: () => show(back[back.length - 1], back.slice(0, -1)) }, "‹") : null,
      h("strong", {}, t.title), h("span", { class: "grow" }),
      h("button", { class: "small icon", title: "All help topics", onclick: () => { close(); openHelpIndex(id); } }, "☰"),
      h("button", { class: "small icon", title: "Close", onclick: close }, "✕")),
    renderBody(t, (rid) => show(rid, [...back, id])));
  place();
}

function openPop(id: TopicId, at: HTMLElement) {
  close();
  anchor = at;
  pop = h("div", { class: "help-pop", role: "dialog", "aria-label": topics[id].title });
  document.body.append(pop);
  show(id, []);
}

document.addEventListener("mousedown", (e) => {
  const t = e.target as Node;
  if (pop && !pop.contains(t) && !anchor?.contains(t)) close();
}, true);
document.addEventListener("keydown", (e) => {
  if (pop && e.key === "Escape") { e.stopPropagation(); close(); }
}, true);
window.addEventListener("resize", close);

// help returns the "?" button for a topic.
export function help(id: TopicId): HTMLButtonElement {
  const b = h("button", { type: "button", class: "help-btn", title: "Help: " + topics[id].title, "aria-label": "Help: " + topics[id].title }, "?");
  b.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (anchor === b) close(); else openPop(id, b);
  });
  return b;
}

// withHelp is a label text followed by its "?" (for field labels and headings).
export function withHelp(label: string, id: TopicId): HTMLElement {
  return h("span", { class: "with-help" }, label, help(id));
}

// openHelpIndex shows every topic on one page.
export function openHelpIndex(focus?: TopicId) {
  const body = h("div", { class: "help-index" });
  const toc = h("div", { class: "help-toc" });
  const sections: Partial<Record<TopicId, HTMLElement>> = {};
  const jump = (id: TopicId) => sections[id]?.scrollIntoView({ behavior: "smooth", block: "start" });
  const groups = new Map<string, TopicId[]>();
  for (const id of Object.keys(topics) as TopicId[]) {
    const g = topics[id].group;
    groups.set(g, [...(groups.get(g) ?? []), id]);
  }
  const content = h("div", { class: "help-content" });
  for (const [g, ids] of groups) {
    toc.append(h("div", { class: "help-toc-group" }, h("div", { class: "muted small" }, g),
      ...ids.map((id) => h("button", { class: "link", onclick: () => jump(id) }, topics[id].title))));
    content.append(h("h4", { class: "help-group" }, g));
    for (const id of ids) {
      const s = h("section", { class: "help-section" }, h("h4", {}, topics[id].title), renderBody(topics[id], jump));
      sections[id] = s;
      content.append(s);
    }
  }
  body.append(
    h("p", { class: "muted small" }, "Canopy reads devices over gNMI and explains what it reads with the YANG models. Mark paths in the schema tree, read them in the Data tab, compare reads with snapshots, and, with network-operator, check and test what the operator does. Every operation shows the gNMIc command that does the same."),
    h("div", { class: "help-cols" }, toc, content));
  modal("Help", body, [], { wide: true });
  // the modal's own close also closes on navigation
  body.querySelectorAll(".help-go").forEach((b) => b.addEventListener("click", () => (body.closest(".modal-back") as HTMLElement | null)?.remove()));
  if (focus) requestAnimationFrame(() => sections[focus]?.scrollIntoView({ block: "start" }));
}
