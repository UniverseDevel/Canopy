import { api, ApiError, catalogApi, GetResult, MatchReport, modelsApi, ModelSource, opApi, OpOptions, reportApi, NodeDetail, Preset, SchemaSummary, Session, SNode, Target, TypeInfo } from "./api";
import { openConfigDialog } from "./config";
import { snapshotsTab } from "./snapshots";
import { operatorTab } from "./operator";
import { auditConsole } from "./console";
import { help, openHelpIndex, setHelpNav, withHelp } from "./help";
import { openCatalog } from "./catalog";
import { openCacheDialog } from "./cachedlg";
import { fixturesTab } from "./fixtures";
import { $, append, clear, copyText, field, h, modal, secretInput, toast } from "./dom";
import { Num, stringify } from "./json";
import { VirtualList } from "./vlist";

// ---------- state ----------

const S = {
  session: null as Session | null, // the active session
  sessions: [] as Session[],
  // per-session working state, restored when switching devices
  perSession: new Map<string, { paths: string; result: GetResult | null; models?: ModelChoice }>(),
  presets: [] as Preset[],
  preset: "", // preset currently loaded into the form
  schema: null as SchemaSummary | null,
  nodes: new Map<number, SNode>(),
  children: new Map<number, number[]>(), // -1 = roots
  expanded: new Set<number>(),
  selected: -1,
  searchResults: null as SNode[] | null,
  result: null as GetResult | null,
};

const store = {
  get(k: string): string { try { return localStorage.getItem("canopy." + k) ?? ""; } catch { return ""; } },
  set(k: string, v: string) { try { localStorage.setItem("canopy." + k, v); } catch { /* storage unavailable */ } },
};

function errText(e: unknown): string {
  if (e instanceof ApiError) {
    const x = e.err;
    let s = `${x.message}`;
    if (x.grpcCode) s += ` (gRPC ${x.grpcCode})`;
    if (x.hint) s += ` — ${x.hint}`;
    return `[${x.code}] ${s}`;
  }
  return String(e);
}

// ---------- layout ----------


const deviceList = h("div", { class: "devices" });
const clusterList = h("div", { class: "devices cluster" });
const clusterInfo = h("div", { class: "muted small cluster-info" }, "not loaded");
// Full-height device column on the far left, like vertical tabs.
const devicePanel = h("aside", { class: "device-panel" },
  h("div", { class: "brand-row" }, h("div", { class: "brand" }, "Canopy"), h("div", { class: "muted small", id: "build" }),
    h("button", { class: "small icon help-index-btn", title: "Help: all topics", onclick: () => openHelpIndex() }, "?")),
  h("button", { class: "primary connect-new", onclick: () => openDeviceDialog() }, "+ Connect device"),
  h("div", { class: "muted small section-label" }, "Devices", help("devices")),
  deviceList,
  h("div", { class: "row between cluster-head" }, h("div", { class: "muted small section-label" }, "Cluster", help("cluster")),
    h("div", { class: "row" }, h("button", { class: "small icon", title: "Reload Devices", onclick: () => loadCluster() }, "↻"),
      h("button", { class: "small icon", title: "Cluster settings", onclick: () => clusterSettings() }, "⚙"))),
  clusterInfo, clusterList);
const activeLabel = h("div", { class: "muted small active-label" }, "No device connected");
let modelSources: ModelSource[] = [];
const modelSrcSel = h("select", { title: "Model source" }, h("option", { value: "" }, "Model source…"));
const modelVerSel = h("select", { title: "Version" }, h("option", { value: "" }, "version"));
const modelLoadBtn = h("button", { class: "small", onclick: () => loadModelSet() }, "Load");
const modelRefreshBtn = h("button", { class: "small icon", title: "Check GitHub for new versions now (the list is otherwise kept for a day)", onclick: () => refreshModelVersions() }, "↻");
const modelStatus = h("div", { class: "muted small model-status" });
const reportLine = h("div", { class: "small report-line" });

const yangInput = h("input", { placeholder: "YANG folder(s), separated by ;", value: store.get("yang") });
const loadBtn = h("button", { onclick: () => loadSchema() }, "Load");
const searchInput = h("input", { type: "search", placeholder: "Search nodes (name, or path with /)" });
const schemaInfo = h("div", { class: "muted small" }, "No models loaded");
const tree = new VirtualList<{ node: SNode; depth: number }>(24, (r) => renderTreeRow(r.node, r.depth));
const searchList = new VirtualList<SNode>(40, (n) => renderSearchRow(n));
const treeHost = h("div", { class: "tree-host" }, tree.el);

const emptyDetails = () => h("div", { class: "empty-state" },
  h("p", {}, "Select a node in the schema tree to see its type, description and path."),
  h("p", { class: "muted small" }, "Tick nodes to mark their paths, then read them in the Data tab. ", help("tree")));
const details = h("div", { class: "details" }, emptyDetails());

const pathsArea = h("textarea", { rows: 4, placeholder: "Paths to read, one per line. Mark nodes in the tree to add them." });
const typeSel = h("select", { title: "Data type" }, ...["all", "config", "state", "operational"].map((t) => h("option", { value: t }, t)));
const encSel = h("select", { title: "Encoding" }, h("option", { value: "" }, "auto"));
const getBtn = h("button", { class: "primary", onclick: () => doGet() }, "Get");
const untickBtn = h("button", { class: "small", title: "Untick all", hidden: true, onclick: () => { dataSel.clear(); dataList.refresh(); syncUntick(); } }, "Untick");
function syncUntick() { untickBtn.hidden = dataSel.size === 0; }
const resultMeta = h("div", { class: "muted small" });
const viewSel = h("div", { class: "seg" });
const dataHost = h("div", { class: "data-host" });
// Qualified paths name the module where it changes (/srl_nokia-system:system/...),
// as devices send them; same-named top nodes of different modules stay apart.
let qualified = store.get("qualified") !== "0";
const qualChk = h("input", { type: "checkbox", checked: qualified });
qualChk.addEventListener("change", () => {
  qualified = qualChk.checked;
  store.set("qualified", qualified ? "1" : "0");
  tree.refresh(); searchList.refresh(); dataList.refresh();
  if (S.selected >= 0) select(S.selected);
});
const defaultShell = /win/i.test(navigator.platform) ? "powershell" : "bash";
function shell(): string { return store.get("shell") || defaultShell; }

function expandData(open: boolean) {
  dataOpen.clear();
  if (open) for (const n of dnodes) if (!n.leaf) dataOpen.add(n.id);
  else if (dnodes.length) dataOpen.add(0);
  dataList.setItems(visibleData());
}
let dataView: "tree" | "json" | "yaml" | "flat" = "tree";

// Command preview: the gNMIc command for what the Data tab reads.
const cmdMode = h("select", { title: "What the command does" },
  h("option", { value: "get" }, "Get"), h("option", { value: "once" }, "Subscribe once"),
  h("option", { value: "stream" }, "Subscribe stream"), h("option", { value: "poll" }, "Subscribe poll"),
  h("option", { value: "capabilities" }, "Capabilities"));
const shellSel = h("select", { title: "Shell syntax" }, h("option", { value: "bash" }, "bash / zsh"), h("option", { value: "powershell" }, "PowerShell"));
shellSel.value = shell();
const cmdInterval = h("input", { value: "10s", size: 5, title: "Sample interval (stream)" });
const cmdText = h("code", { class: "cmd-line", title: "Click to copy" });
const cmdNote = h("div", { class: "muted small cmd-note" });
const cmdBox = h("div", { class: "cmd-preview" },
  h("div", { class: "cmd-bar" }, h("span", { class: "cmd-label" }, "gNMIc"), help("command"), cmdMode, cmdInterval, shellSel,
    h("span", { class: "grow" }),
    h("button", { class: "small", onclick: () => cmdText.textContent && copyText(cmdText.textContent, "command") }, "Copy")),
  cmdText, cmdNote);
cmdText.addEventListener("click", () => cmdText.textContent && copyText(cmdText.textContent, "command"));

const capsHost = h("div", { class: "caps" }, h("p", { class: "muted" }, "Connect to see capabilities."));
const footer = h("footer", {});
const consoleUI = auditConsole({ shell });

const tabs = ["Data", "Snapshots", "Operator", "Fixtures"] as const;
const fxTab = fixturesTab({ session: () => S.session, options: () => opOptions() });
const opTab = operatorTab({ session: () => S.session, options: () => opOptions(), toFixture: (r) => { showTab("Fixtures"); fxTab.useResource(r); } });
const snaps = snapshotsTab({ session: () => S.session, paths: () => markedPaths(), type: () => typeSel.value });
type Tab = typeof tabs[number];
const tabBar = h("div", { class: "tabs" });
const tabBodies: Record<Tab, HTMLElement> = {
  Data: h("div", { class: "tab-body data-tab" },
    h("div", { class: "data-top" },
      h("div", { class: "get-form" }, pathsArea,
        h("div", { class: "row" }, typeSel, encSel, getBtn,
          h("button", { title: "Remove all paths (unmarks them in the tree)", onclick: () => { setPaths([]); } }, "Clear"), help("read"))),
      h("div", { class: "toolbar" }, viewSel,
        h("button", { class: "small icon", title: "Expand all", onclick: () => expandData(true) }, "⊞"),
        h("button", { class: "small icon", title: "Collapse all", onclick: () => expandData(false) }, "⊟"),
        h("span", { class: "grow" }),
        h("span", { class: "seg" },
          h("button", { title: "Copy as JSON: the ticked data, or everything when nothing is ticked", onclick: () => copySmart("json") }, "Copy JSON"),
          h("button", { title: "Copy as YAML: the ticked data, or everything when nothing is ticked", onclick: () => copySmart("yaml") }, "YAML")),
        untickBtn, help("dataView")),
      resultMeta,
      dataHost),
    cmdBox),
  Snapshots: h("div", { class: "tab-body" }, snaps.el),
  Operator: h("div", { class: "tab-body" }, opTab.el),
  Fixtures: h("div", { class: "tab-body" }, fxTab.el),
};
let activeTab: Tab = "Data";

function showTab(t: Tab) {
  activeTab = t;
  clear(tabBar);
  for (const x of tabs) {
    tabBar.append(h("button", { class: x === t ? "tab active" : "tab", onclick: () => showTab(x) }, x));
  }
  for (const x of tabs) tabBodies[x].hidden = x !== t;
  if (t === "Snapshots") snaps.refresh();
  if (t === "Operator") opTab.refresh();
  if (t === "Fixtures") fxTab.refresh();
}

// Left pane: the schema tree, or the device's capabilities (device-global).
const leftTabsDef = ["Schema", "Capabilities"] as const;
type LeftTab = typeof leftTabsDef[number];
const leftTabBar = h("div", { class: "tabs" });
const leftBodies = {} as Record<LeftTab, HTMLElement>;
function showLeftTab(t: LeftTab) {
  clear(leftTabBar);
  for (const x of leftTabsDef) leftTabBar.append(h("button", { class: x === t ? "tab active" : "tab", onclick: () => showLeftTab(x) }, x));
  for (const x of leftTabsDef) leftBodies[x].hidden = x !== t;
}

// ---------- layout: resizable panes, collapsible connection form ----------

const localRow = h("div", { class: "row local-models", hidden: true }, yangInput, loadBtn);
const markedOnlyBtn = h("button", { class: "small toggle", title: "Show only the marked paths (and what is under them)", onclick: () => setMarkedOnly(!markedOnly) }, "Marked only");
leftBodies.Schema = h("div", { class: "left-body" },
  h("div", { class: "models-panel" },
    h("div", { class: "row models-row" }, h("strong", {}, "Models"), help("models"), modelSrcSel, modelVerSel, modelRefreshBtn, modelLoadBtn),
    modelStatus, reportLine,
    h("div", { class: "row model-actions" },
      h("button", { class: "link small", title: "Search the modules of all indexed releases", onclick: () => openCatalog({ sources: () => modelSources, load: (s, v) => selectModelUI(s, v).then(() => loadModelSet(s, v)) }) }, "Catalog"),
      h("button", { class: "link small", title: "Download the device's own YANG modules over NETCONF (get-schema)", onclick: () => modelsFromDevice() }, "From device"),
      h("button", { class: "link small", title: "See, remove and share the downloaded model sets", onclick: () => openCacheDialog({
        sources: () => modelSources,
        loaded: () => modelSrcSel.value && modelVerSel.value ? { source: modelSrcSel.value, version: modelVerSel.value } : null,
        changed: () => { if (modelSrcSel.value) fillModelVersions(modelVerSel.value); },
      }) }, "Downloaded"),
      h("button", { class: "link small", title: "Load .yang files from a folder on this computer", onclick: () => { localRow.hidden = !localRow.hidden; if (!localRow.hidden) yangInput.focus(); } }, "Local folder")),
    localRow),
  schemaInfo,
  h("div", { class: "tree-tools" }, searchInput, markedOnlyBtn,
    h("label", { class: "check small", title: "Name modules where they change, e.g. /srl_nokia-system:system (OpenConfig paths also get the openconfig: origin). Same-named nodes of different modules stay apart." }, qualChk, "Qualified"), help("tree")),
  treeHost);
leftBodies.Capabilities = h("div", { class: "left-body" },
  h("div", { class: "row caps-head" }, h("strong", {}, "Capabilities"), help("capabilities")), capsHost);
const leftPane = h("section", { class: "pane left" }, leftTabBar, leftBodies.Schema, leftBodies.Capabilities);
const centerPane = h("section", { class: "pane center" }, details);
const rightPane = h("section", { class: "pane right" }, tabBar, ...tabs.map((t) => tabBodies[t]));
const splitL = h("div", { class: "split", title: "Drag to resize" });
const splitR = h("div", { class: "split", title: "Drag to resize" });
const mainEl = h("main", {}, leftPane, splitL, centerPane, splitR, rightPane);

function setupSplitters() {
  const apply = () => {
    const l = Number(store.get("wLeft")), r = Number(store.get("wRight"));
    if (l) mainEl.style.setProperty("--w-left", `${l}px`);
    if (r) mainEl.style.setProperty("--w-right", `${r}px`);
  };
  const drag = (which: "wLeft" | "wRight") => (e: MouseEvent) => {
    e.preventDefault();
    const box = mainEl.getBoundingClientRect();
    const move = (ev: MouseEvent) => {
      const w = which === "wLeft" ? ev.clientX - box.left : box.right - ev.clientX;
      const max = box.width - 520; // keep room for the other panes
      store.set(which, String(Math.round(Math.max(220, Math.min(max, w)))));
      apply();
    };
    const up = () => { document.removeEventListener("mousemove", move); document.removeEventListener("mouseup", up); document.body.classList.remove("dragging"); };
    document.body.classList.add("dragging");
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  };
  splitL.addEventListener("mousedown", drag("wLeft"));
  splitR.addEventListener("mousedown", drag("wRight"));
  // double-click resets
  for (const [s, k] of [[splitL, "wLeft"], [splitR, "wRight"]] as const) {
    s.addEventListener("dblclick", () => { store.set(k, ""); mainEl.style.removeProperty(k === "wLeft" ? "--w-left" : "--w-right"); });
  }
  apply();
}

function mount() {
  const app = $("#app");
  append(app,
    devicePanel,
    h("div", { class: "workspace" },
      h("header", {}, activeLabel),
      mainEl,
      consoleUI.el,
      footer));
  setupSplitters();
  showTab("Data");
  showLeftTab("Schema");
  setHelpNav((t) => {
    if (t === "console") { if (consoleUI.el.hidden) consoleUI.btn.click(); return; }
    const [kind, name] = t.split(":");
    if (kind === "tab") showTab(name as Tab);
    else showLeftTab(name as LeftTab);
  });
  renderViewSel();
  let searchTimer = 0;
  searchInput.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = window.setTimeout(() => search(searchInput.value), 200);
  });
  pathsArea.addEventListener("input", () => pathsChanged());
  for (const el of [typeSel, encSel, cmdMode, cmdInterval]) el.addEventListener("change", () => updateCmd());
  shellSel.addEventListener("change", () => { store.set("shell", shellSel.value); updateCmd(); consoleUI.redraw(); });
  yangInput.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Enter") loadSchema(); });
  api.info().then((i) => ($("#build").textContent = `${i.build.version} ${i.build.commit.slice(0, 8)}`)).catch(() => {});
  api.schema().then((s) => { if (s) schemaLoaded(s); }).catch(() => {});
  initModels();
  opApi.info().then((i) => { opDefaults = i.defaults ?? {}; }).catch(() => {});
  // Sessions live in the server: after a page reload, pick them up again.
  Promise.allSettled([loadDevices(), loadCluster()]).then(() => restoreSessions());
  renderFooter();
  updateCmd();
}


// ---------- modal ----------


// ---------- devices ----------

// A device in the left list: a saved preset (key "p:name") or one added in this
// browser session (key "a:n"). Passwords stay in memory only.
interface Device {
  key: string;
  name: string;
  preset?: Preset;
  target: Target;
  models?: string;
  yangDirs?: string[];
  paths?: string[];
  description?: string;
  session?: Session;
  state: "off" | "busy" | "on" | "err";
  error?: string;
  password?: string;
  k8s?: { namespace: string; name: string; ready: string; paused: boolean; provider: string };
}
const devices: Device[] = [];
let selectedKey = "";
let adhocSeq = 0;

function presetTarget(p: Preset): Target {
  return {
    address: p.address, insecure: p.insecure, skipVerify: p.skipVerify, caFile: p.caFile, serverName: p.serverName,
    username: p.username, timeoutSec: p.timeoutSec, jumps: p.jumps, identityFiles: p.identityFiles, preset: p.name,
  };
}

async function loadDevices() {
  try {
    S.presets = await api.presets();
  } catch (e) {
    toast(errText(e), "err");
    return;
  }
  const old = new Map(devices.map((d) => [d.key, d]));
  const next: Device[] = [];
  for (const p of S.presets) {
    const key = "p:" + p.name;
    const d = old.get(key) ?? { key, name: p.name, target: presetTarget(p), state: "off" as const };
    Object.assign(d, { name: p.name, preset: p, target: presetTarget(p), models: p.models, yangDirs: p.yangDirs, paths: p.paths, description: p.description });
    next.push(d);
  }
  for (const d of devices) if (d.key.startsWith("a:")) next.push(d);
  // keep devices that are still connected even if their preset was deleted
  for (const d of devices) if (d.session && !next.includes(d)) next.push(d);
  devices.splice(0, devices.length, ...next);
  renderDevices();
}

function renderDevices() {
  clear(deviceList);
  clear(clusterList);
  const local = devices.filter((d) => !d.k8s);
  if (!local.length) {
    deviceList.append(h("p", { class: "muted small pad" }, "No devices yet. Use + Connect device."));
  }
  for (const d of devices) {
    const on = d.state === "on";
    // Compact icon button with a fixed size, so nothing moves when it changes.
    const btn = h("button", {
      class: "act" + (on ? " stop" : d.state === "busy" ? "" : " primary"), disabled: d.state === "busy",
      title: d.state === "busy" ? "Connecting…" : on ? "Disconnect" : "Connect",
      "aria-label": d.state === "busy" ? "Connecting" : on ? "Disconnect" : "Connect",
      onclick: (e: Event) => { e.stopPropagation(); on ? disconnectDevice(d) : connectDevice(d); },
    }, d.state === "busy" ? "⋯" : on ? "■" : "▶");
    const edit = d.k8s ? h("span", { class: "icon-spacer" }) : h("button", {
      class: "small icon", title: "Edit",
      onclick: (e: Event) => { e.stopPropagation(); openDeviceDialog(d); },
    }, "✎");
    const sub = d.target.address + (d.target.jumps?.length ? ` via ${d.target.jumps.join(", ")}` : "");
    const row = h("div", { class: "device" + (d.key === selectedKey ? " sel" : "") + (on ? " on" : "") },
      h("span", { class: "dot " + d.state, title: { off: "not connected", busy: "connecting", on: "connected", err: "error" }[d.state] }),
      h("div", { class: "dev-main" },
        h("div", { class: "dev-name" }, d.name,
          !d.preset && !d.k8s ? h("span", { class: "tag" }, "not saved") : null,
          d.k8s ? h("span", { class: "tag " + (d.k8s.ready === "True" ? "tag-ok" : "tag-warn"), title: "Ready condition of the Device" }, d.k8s.ready === "True" ? "ready" : "not ready") : null,
          d.k8s?.paused ? h("span", { class: "tag tag-warn" }, "paused") : null),
        h("div", { class: "muted small dev-sub" }, sub),
        d.state === "err" && d.error ? h("div", { class: "err small dev-sub dev-err", title: d.error + "\n\nClick for the full error in the Console" },
          h("button", { class: "link small", onclick: (ev: Event) => { ev.stopPropagation(); consoleUI.reveal(); } }, "Failed: see Console"), " ", d.error) : null),
      edit, btn);
    row.title = [d.description, d.preset?.lastSeen ? `last connected ${new Date(d.preset.lastSeen.time).toLocaleString()}` : ""].filter(Boolean).join("\n");
    row.addEventListener("click", () => selectDevice(d));
    row.addEventListener("dblclick", () => { if (d.state === "off" || d.state === "err") connectDevice(d); });
    (d.k8s ? clusterList : deviceList).append(row);
  }
}

// ---------- cluster (operator mode) ----------

let opDefaults: OpOptions = {};

function opOptions(): OpOptions {
  const o: OpOptions = {};
  const kc = store.get("op.kubeconfig"), cx = store.get("op.context"), j = store.get("op.jumps"), id = store.get("op.identity");
  if (kc) o.kubeconfig = kc;
  if (cx) o.context = cx;
  if (j) o.jumps = j.split(",").map((s) => s.trim()).filter(Boolean);
  if (id) o.identityFiles = [id];
  return o;
}

async function loadCluster() {
  clusterInfo.textContent = "Loading Devices…";
  clusterInfo.title = "";
  try {
    const r = await opApi.devices(opOptions());
    clusterInfo.textContent = `${r.context} · ${r.devices.length} Device${r.devices.length === 1 ? "" : "s"}`;
    const old = new Map(devices.filter((d) => d.k8s).map((d) => [d.key, d]));
    for (const k of old.keys()) devices.splice(devices.findIndex((d) => d.key === k), 1);
    for (const x of r.devices) {
      const key = `k:${x.namespace}/${x.name}`;
      const d = old.get(key) ?? { key, name: "", target: { address: "" }, state: "off" as const };
      Object.assign(d, {
        name: `${x.namespace}/${x.name}`, target: { address: x.address, jumps: opOptions().jumps ?? opDefaults.jumps },
        description: `${x.provider}\nphase ${x.phase ?? "?"}`,
        k8s: { namespace: x.namespace, name: x.name, ready: x.ready, paused: x.paused, provider: x.provider },
      });
      devices.push(d);
    }
    // keep connected cluster devices that disappeared from the list
    for (const [k, d] of old) if (d.session && !devices.some((x) => x.key === k)) devices.push(d);
  } catch (e) {
    const full = errText(e);
    clusterInfo.title = full;
    clusterInfo.textContent = /kubeconfig/i.test(full) ? "No cluster: no kubeconfig found. ⚙ to choose one." : full;
  }
  renderDevices();
}

async function clusterSettings() {
  let info: Awaited<ReturnType<typeof opApi.info>> | null = null;
  try { info = await opApi.info(); } catch { /* shown below */ }
  const d = info?.defaults ?? {};
  const kc = h("input", { value: store.get("op.kubeconfig"), placeholder: d.kubeconfig || "~/.kube/config" });
  const cx = h("select", {}, h("option", { value: "" }, `current (${info?.current ?? "?"})`),
    ...(info?.contexts ?? []).map((c) => h("option", { value: c }, c)));
  cx.value = store.get("op.context");
  const j = h("input", { value: store.get("op.jumps"), placeholder: (d.jumps ?? []).join(", ") || "user@jump:22 (optional)" });
  const id = h("input", { value: store.get("op.identity"), placeholder: (d.identityFiles ?? [])[0] || "key file (optional)" });
  const r = await modal("Cluster (operator mode)", h("div", { class: "form" },
    h("p", { class: "muted small" }, "Canopy reads network-operator's Devices from this cluster and connects to them as the operator does (address, credentials and CA from the Device's Secrets). Device addresses are often only reachable from the cluster network; give a jump host for that. Empty fields use the defaults Canopy was started with."),
    h("div", { class: "form-grid" }, field("Kubeconfig", kc), field("Context", cx), field(withHelp("SSH jump hosts to devices", "jump"), j), field("SSH key file", id)),
    info?.error ? h("p", { class: "err small" }, info.error) : null),
    [{ label: "Save and reload", primary: true, value: "ok" }]);
  if (r !== "ok") return;
  store.set("op.kubeconfig", kc.value.trim());
  store.set("op.context", cx.value);
  store.set("op.jumps", j.value.trim());
  store.set("op.identity", id.value.trim());
  loadCluster();
}

async function selectDevice(d: Device) {
  selectedKey = d.key;
  renderDevices();
  updateCmd();
  if (d.session && S.session?.id !== d.session.id) await activate(d.session.id);
}

// rememberSelect chooses where a saved device's password is remembered.
function rememberSelect(current?: string) {
  return h("select", {},
    h("option", { value: "" }, current ? "Keep as it is" : "Don't remember it"),
    h("option", { value: "keychain" }, "In the OS keychain"),
    h("option", { value: "file" }, "In an encrypted file next to the connections file (git-ignored)"));
}
const storeName = (s?: string) => s === "file" ? "the encrypted password file" : s === "keychain" ? "the OS keychain" : "";

// passwordPrompt asks for a password; with remember, also where to keep it.
function passwordPrompt(title: string, note: string, remember = false): Promise<{ value: string; store: string } | null> {
  const inp = secretInput({ placeholder: "password" });
  const sel = remember ? rememberSelect() : null;
  return new Promise((res) => {
    const done = () => ({ value: inp.value, store: sel?.value ?? "" });
    inp.addEventListener("keydown", (ev: KeyboardEvent) => {
      if (ev.key === "Enter") { (inp.closest(".modal-back") as HTMLElement).remove(); res(done()); }
    });
    modal(title, h("div", { class: "form" }, h("p", { class: "muted small" }, note), inp,
      sel ? field(withHelp("Remember it (once it works)", "keychain"), sel) : null), [{ label: "OK", primary: true, value: "ok" }])
      .then((r) => res(r === "ok" ? done() : null));
  });
}

async function connectDevice(d: Device) {
  d.state = "busy";
  d.error = "";
  renderDevices();
  const t: Target = { ...d.target, password: d.password };
  let rememberAs = ""; // where to keep a password typed at the prompt, once it works
  try {
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        const s = d.k8s
          ? await opApi.connect(opOptions(), d.k8s.namespace, d.k8s.name, { acceptHostKeys: t.acceptHostKeys, saveHostKeys: t.saveHostKeys, passphrases: t.passphrases })
          : await api.connect(t);
        d.session = s;
        d.state = "on";
        if (rememberAs && d.preset && t.password) {
          api.presetPassword({ name: d.preset.name, password: t.password, store: rememberAs })
            .then((r) => { toast(`Password ${r.message}`); loadDevices(); }).catch((e) => toast(errText(e), "err"));
        }
        S.sessions.push(s);
        selectedKey = d.key;
        renderDevices();
        await activate(s.id);
        if (d.preset) loadDevices(); // refresh "last connected"
        return;
      } catch (e) {
        if (!(e instanceof ApiError)) throw e;
        const x = e.err.details ?? {};
        if (e.err.code === "host_key_unknown") {
          const ok = await modal("Unknown SSH host key",
            h("div", {}, h("p", {}, `The host key of ${x.jumpHost} is not in known_hosts.`),
              h("pre", { class: "fp" }, `${x.keyType}\n${x.fingerprint}`),
              h("p", { class: "muted small" }, "Compare it with the fingerprint from the host's administrator. Trusting adds it to known_hosts.")),
            [{ label: "Trust and connect", primary: true, value: "yes" }]);
          if (ok !== "yes") throw e;
          t.acceptHostKeys = [...(t.acceptHostKeys ?? []), x.fingerprint];
          t.saveHostKeys = true;
          continue;
        }
        if (e.err.code === "passphrase_required") {
          const v = await passwordPrompt(`Passphrase for ${x.keyFile}`, "The SSH key is protected by a passphrase. It is kept in memory only.");
          if (v === null) throw e;
          t.passphrases = { ...(t.passphrases ?? {}), [x.keyFile]: v.value };
          continue;
        }
        if (e.err.code === "auth_failed") {
          const v = await passwordPrompt(`Password for ${t.username || "the device"} on ${d.name}`,
            t.password ? "The password was not accepted. Try again." :
              "No password was set for this device (or the one from the environment or a stored one was not accepted).",
            !!d.preset);
          if (v === null) throw e;
          t.password = v.value;
          d.password = v.value;
          rememberAs = v.store;
          continue;
        }
        throw e;
      }
    }
  } catch (e) {
    d.state = "err";
    d.error = errText(e);
    renderDevices();
  }
}

async function disconnectDevice(d: Device) {
  if (d.session) await disconnect(d.session.id);
  d.session = undefined;
  d.state = "off";
  renderDevices();
}

// openDeviceDialog adds a device or edits one. Saved devices live in the
// connections file; "Connect without saving" makes a temporary one.
async function openDeviceDialog(edit?: Device) {
  const p = edit?.preset;
  const t = edit?.target;
  const v = (x?: string) => x ?? "";
  const name = h("input", { value: edit ? edit.name : "", placeholder: "e.g. leaf1 (the target if empty)" });
  const address = h("input", { value: v(t?.address), placeholder: "host:port, e.g. 10.0.0.5:57400" });
  const tls = h("select", {},
    h("option", { value: "verify" }, "TLS, verify certificate"),
    h("option", { value: "skip" }, "TLS, skip verification"),
    h("option", { value: "plain" }, "Plaintext (insecure)"));
  tls.value = t ? (t.insecure ? "plain" : t.skipVerify ? "skip" : "verify") : "skip";
  const ca = h("input", { value: v(t?.caFile), placeholder: "CA file (optional)" });
  const serverName = h("input", { value: v(t?.serverName), placeholder: "server name override (optional)" });
  const username = h("input", { value: v(t?.username), placeholder: "username", autocomplete: "off", "data-1p-ignore": "true", "data-lpignore": "true" });
  const password = secretInput({ value: v(edit?.password), placeholder: "kept in memory only" });
  const jumps = h("input", { value: (t?.jumps ?? []).join(", "), placeholder: "user@jump1:22, jump2 (optional)" });
  const identity = h("input", { value: (t?.identityFiles ?? [])[0] ?? "", placeholder: "key file (optional)" });
  const timeout = h("input", { type: "number", min: "1", value: String(t?.timeoutSec || 10) });
  const desc = h("input", { value: v(edit?.description), placeholder: "optional" });
  const [mSrc, mVer] = (edit?.models ?? "").split("@");
  const modelSrc = h("select", {}, h("option", { value: "" }, "Detect from the device"),
    ...modelSources.map((s) => h("option", { value: s.id }, s.name)));
  modelSrc.value = mSrc ?? "";
  const modelVer = h("select", {}, h("option", { value: "" }, "—"));
  const fillVersions = async () => {
    clear(modelVer);
    if (!modelSrc.value) { modelVer.append(h("option", { value: "" }, "—")); return; }
    modelVer.append(h("option", { value: "" }, "loading…"));
    try {
      const vs = await modelsApi.versions(modelSrc.value);
      clear(modelVer);
      modelVer.append(...vs.map((x) => h("option", { value: x.name }, x.name + (x.cached ? "  ✓" : ""))));
      if (mVer && modelSrc.value === mSrc) modelVer.value = mVer;
    } catch (e) {
      clear(modelVer);
      modelVer.append(h("option", { value: "" }, "listing failed"));
      toast(errText(e), "err");
    }
  };
  modelSrc.addEventListener("change", fillVersions);
  if (modelSrc.value) fillVersions();
  const pwEnv = h("input", { value: v(p?.passwordEnv), placeholder: "GNMI_PASSWORD" });
  const remember = rememberSelect(p?.passwordStored);
  const forget = h("input", { type: "checkbox" });
  const stored = p?.passwordStored ? `A password is stored in ${storeName(p.passwordStored)}.` : "";
  const rememberFields = h("div", { class: "form" },
    h("div", { class: "form-grid" }, field(withHelp("Remember the typed password", "keychain"), remember, stored || "Never in the connections file itself.")),
    p?.passwordStored ? h("label", { class: "check" }, forget, ` Forget the password stored in ${storeName(p.passwordStored)}`) : null);
  const body = h("div", { class: "form" },
    h("div", { class: "form-grid" },
      field("Name", name), field(withHelp("Target", "connect"), address), field(withHelp("Security", "tls"), tls), field("Timeout (s)", timeout),
      field("CA file", ca), field("TLS server name", serverName), field("Username", username), field("Password", password),
      field(withHelp("SSH jump hosts", "jump"), jumps, "comma-separated chain"), field("SSH key file", identity),
      field(withHelp("YANG models", "models"), modelSrc), field("Model version", modelVer),
      field("Description", desc), field("Password environment variable", pwEnv, "Used when no password is typed.")),
    rememberFields,
    h("p", { class: "muted small" }, p?.source ? `Saved in ${p.source}. Passwords never go into that file.` : "Saved devices go into your connections file. Passwords never go into it."));
  const buttons = [{ label: "Save and connect", primary: true, value: "connect" }, { label: "Save", value: "save" }];
  if (!p) buttons.push({ label: "Connect without saving", value: "once" });
  if (edit) buttons.push({ label: p ? "Delete" : "Remove", value: "remove" });
  const title = !edit ? "Connect device" : `Edit “${edit.name}”`;
  for (;;) {
    const r = await modal(title, body, buttons);
    if (r === null) return;
    if (r === "remove") {
      if (p && (await modal(`Delete “${p.name}”?`, h("p", {}, `It is removed from ${p.source ?? "your connections file"}.`),
        [{ label: "Delete", primary: true, value: "yes" }])) !== "yes") continue;
      if (edit!.session) await disconnectDevice(edit!);
      if (edit!.preset) {
        try { await api.deletePreset(edit!.preset.name); } catch (e) { toast(errText(e), "err"); return; }
      }
      devices.splice(devices.indexOf(edit!), 1);
      await loadDevices();
      return;
    }
    if (!address.value.trim()) { toast("Enter a target address", "err"); continue; }
    const save = r !== "once";
    const jl = jumps.value.split(",").map((s) => s.trim()).filter(Boolean);
    const tgt: Target = {
      address: address.value.trim(), insecure: tls.value === "plain", skipVerify: tls.value === "skip",
      caFile: tls.value === "verify" ? ca.value.trim() || undefined : undefined,
      serverName: tls.value !== "plain" ? serverName.value.trim() || undefined : undefined,
      username: username.value.trim() || undefined, timeoutSec: Number(timeout.value) || 10,
      jumps: jl.length ? jl : undefined, identityFiles: identity.value.trim() ? [identity.value.trim()] : undefined,
    };
    const models = modelSrc.value && modelVer.value ? `${modelSrc.value}@${modelVer.value}` : undefined;
    let d: Device;
    if (save) {
      const preset: Preset = {
        ...(p ?? {}), // fields the dialog doesn't show (encoding, client certificate, ...) stay as they are
        name: name.value.trim() || tgt.address.replace(/[^A-Za-z0-9 ._/-]/g, "_"), source: p?.source, description: desc.value.trim() || undefined, passwordEnv: pwEnv.value.trim() || undefined,
        address: tgt.address, insecure: tgt.insecure, skipVerify: tgt.skipVerify, caFile: tgt.caFile, serverName: tgt.serverName,
        username: tgt.username, timeoutSec: tgt.timeoutSec, jumps: tgt.jumps, identityFiles: tgt.identityFiles,
        models, paths: edit?.paths, yangDirs: edit?.yangDirs, tags: p?.tags,
      };
      try {
        if (remember.value && !password.value) { toast("Type the password to remember", "err"); continue; }
        const saved = await api.savePreset(preset, { password: remember.value ? password.value : undefined, rememberPassword: !!remember.value, passwordStore: remember.value || undefined, forgetPassword: forget.checked });
        if (remember.value) toast(`Password stored in ${storeName(remember.value)}`);
        if (p && p.name.toLowerCase() !== saved.name.toLowerCase()) await api.deletePreset(p.name).catch(() => {}); // renamed
        if (edit && !edit.preset) devices.splice(devices.indexOf(edit), 1); // unsaved device became saved
        await loadDevices();
        d = devices.find((x) => x.key === "p:" + saved.name)!;
      } catch (e) {
        toast(errText(e), "err");
        continue;
      }
    } else if (edit && !edit.preset) {
      d = edit;
      Object.assign(d, { name: name.value.trim() || tgt.address, target: tgt, models, description: desc.value.trim() || undefined });
    } else {
      d = { key: `a:${++adhocSeq}`, name: name.value.trim() || tgt.address, target: tgt, models, state: "off" };
      devices.push(d);
    }
    if (password.value) d.password = password.value;
    selectedKey = d.key;
    renderDevices();
    if (r === "connect" || r === "once") await connectDevice(d);
    return;
  }
}

// ---------- models ----------


async function initModels() {
  try {
    modelSources = await modelsApi.sources();
  } catch { return; }
  modelSrcSel.append(...modelSources.map((s) => h("option", { value: s.id, title: s.description ?? "" }, s.name)));
  modelSrcSel.addEventListener("change", () => fillModelVersions());
}

async function fillModelVersions(select?: string, refresh = false) {
  clear(modelVerSel);
  if (!modelSrcSel.value) { modelVerSel.append(h("option", { value: "" }, "version")); return; }
  modelVerSel.append(h("option", { value: "" }, "loading…"));
  try {
    const vs = await modelsApi.versions(modelSrcSel.value, refresh);
    clear(modelVerSel);
    modelVerSel.append(...vs.map((x) => h("option", { value: x.name, title: x.cached ? "downloaded" : "" }, x.name + (x.cached ? "  ✓" : ""))));
    if (select) modelVerSel.value = select;
  } catch (e) {
    clear(modelVerSel);
    modelVerSel.append(h("option", { value: "" }, "listing failed"));
    setModelStatus(errText(e), true);
  }
}

// refreshModelVersions lists the source's versions from GitHub again. When the
// active device's models were a fallback (nearest older or newest), it detects
// again, so a release published since then is picked.
async function refreshModelVersions() {
  const src = modelSrcSel.value;
  if (!src) { toast("Pick a model source first", "err"); return; }
  const before = [...modelVerSel.options].map((o) => o.value).filter(Boolean);
  modelRefreshBtn.disabled = true;
  try {
    await fillModelVersions(modelVerSel.value, true);
    const after = [...modelVerSel.options].map((o) => o.value).filter(Boolean);
    const added = after.filter((v) => !before.includes(v));
    toast(added.length ? `${added.length} new version${added.length === 1 ? "" : "s"}: ${added.slice(0, 3).join(", ")}${added.length > 3 ? "…" : ""}` : "No new versions");
    const s = S.session;
    const prev = s ? S.perSession.get(s.id)?.models : undefined;
    if (s && prev?.kind === "set" && prev.source === src && /^(nearest|newest)/.test(prev.note)) {
      S.perSession.set(s.id, { ...S.perSession.get(s.id)!, models: undefined });
      await modelsForSession(s);
    }
  } finally {
    modelRefreshBtn.disabled = false;
  }
}

function setModelStatus(text: string, err = false) {
  modelStatus.textContent = text;
  modelStatus.className = (err ? "err" : "muted") + " small model-status";
}

type ModelChoice = { kind: "set"; source: string; version: string; note: string } | { kind: "dirs"; dirs: string[] };

async function loadModelSet(src = modelSrcSel.value, ver = modelVerSel.value, note = ""): Promise<boolean> {
  if (!src || !ver) { toast("Pick a model source and version", "err"); return false; }
  modelLoadBtn.disabled = true;
  const name = modelSources.find((s) => s.id === src)?.name ?? src;
  setModelStatus(`Loading ${name} ${ver} (downloading from GitHub the first time)…`);
  try {
    const r = await modelsApi.load(src, ver);
    if (r.schema) await schemaLoaded(r.schema);
    setModelStatus(`${name} ${ver}${note ? " · " + note : ""}`);
    rememberModels({ kind: "set", source: src, version: ver, note });
    return true;
  } catch (e) {
    setModelStatus(errText(e), true);
    return false;
  } finally {
    modelLoadBtn.disabled = false;
  }
}

// modelsFromDevice downloads the modules the device implements over NETCONF.
async function modelsFromDevice() {
  const s = S.session;
  if (!s) { toast("Connect a device first", "err"); return; }
  const port = h("input", { type: "number", value: "830", min: "1", max: "65535" });
  const host = h("input", { placeholder: "the device's address" });
  const refresh = h("input", { type: "checkbox" });
  const r = await modal("YANG models from the device", h("div", { class: "form" },
    h("p", { class: "muted small" }, `Opens NETCONF over SSH on ${s.label} with the same user, password and jump hosts, lists the schemas the device offers and downloads each with get-schema. Read only. `, help("netconf")),
    h("div", { class: "form-grid" }, field("NETCONF port", port), field("Host (optional)", host)),
    h("label", { class: "check" }, refresh, " Download again (otherwise an earlier download is reused)")),
    [{ label: "Download and load", primary: true, value: "ok" }]);
  if (r !== "ok") return;
  const req = { session: s.id, port: Number(port.value) || 830, host: host.value.trim() || undefined, refresh: refresh.checked, acceptHostKeys: [] as string[], saveHostKeys: false };
  setModelStatus(`Downloading the models from ${s.label} over NETCONF…`);
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const res = await catalogApi.fromDevice(req);
      if (res.schema) await schemaLoaded(res.schema);
      const note = `from the device over NETCONF · ${res.files} modules${res.cached ? " (earlier download)" : ""}`;
      setModelStatus(note + (res.skipped?.length ? ` · ${res.skipped.length} skipped` : ""));
      rememberModels({ kind: "dirs", dirs: [res.dir] });
      return;
    } catch (e) {
      if (e instanceof ApiError && e.err.code === "host_key_unknown") {
        const x = e.err.details ?? {};
        const ok = await modal("Unknown SSH host key",
          h("div", {}, h("p", {}, `The NETCONF host key of ${x.jumpHost} is not in known_hosts.`),
            h("pre", { class: "fp" }, `${x.keyType}\n${x.fingerprint}`),
            h("p", { class: "muted small" }, "Compare it with the fingerprint from the device's administrator. Trusting adds it to known_hosts.")),
          [{ label: "Trust and continue", primary: true, value: "yes" }]);
        if (ok !== "yes") { setModelStatus("Cancelled", true); return; }
        req.acceptHostKeys.push(x.fingerprint);
        req.saveHostKeys = true;
        continue;
      }
      setModelStatus(errText(e), true);
      return;
    }
  }
}

function rememberModels(c: ModelChoice) {
  if (!S.session) return;
  const st = S.perSession.get(S.session.id) ?? { paths: pathsArea.value, result: S.result };
  st.models = c;
  S.perSession.set(S.session.id, st);
}

async function selectModelUI(src: string, ver: string) {
  modelSrcSel.value = src;
  await fillModelVersions(ver);
}

// modelsForSession loads the models for the active session: what was used last
// time, the device's saved folders or model set, or auto-detection.
async function modelsForSession(s: Session) {
  const prev = S.perSession.get(s.id)?.models;
  if (prev?.kind === "set") { await selectModelUI(prev.source, prev.version); await loadModelSet(prev.source, prev.version, prev.note); return; }
  if (prev?.kind === "dirs") { yangInput.value = prev.dirs.join("; "); await loadSchema(); return; }
  if (s.yangDirs?.length) { yangInput.value = s.yangDirs.join("; "); await loadSchema(); return; }
  if (s.models?.includes("@")) {
    const [src, ver] = s.models.split("@");
    await selectModelUI(src, ver);
    await loadModelSet(src, ver, "from the saved device");
    return;
  }
  setModelStatus("Detecting the platform…");
  // When this device's models can't be loaded, say whose models the tree shows.
  const stale = () => S.schema && schemaFor && schemaFor !== s.id
    ? ` The tree still shows the models of ${S.sessions.find((x) => x.id === schemaFor)?.label ?? "another device"}; pick models for this device.` : "";
  try {
    const r = await modelsApi.auto(s.id);
    const d = r.detection;
    if (r.error) { setModelStatus(errText(new ApiError(r.error, 0)) + stale(), true); return; }
    if (!d?.source) { setModelStatus((d?.reason ?? "Platform not recognized; pick models below.") + stale(), true); return; }
    if (!d.version) {
      await selectModelUI(d.source, "");
      setModelStatus(`${d.reason}.${stale()}`, true);
      return;
    }
    const note = d.exact ? `detected (device ${d.deviceVersion ?? ""})`.replace(" ()", "")
      : d.want ? `nearest to ${d.want} (device ${d.deviceVersion ?? "?"})` : "newest release (device version unknown)";
    await selectModelUI(d.source, d.version);
    if (r.schema) await schemaLoaded(r.schema);
    const name = modelSources.find((x) => x.id === d.source)?.name ?? d.source;
    setModelStatus(`${name} ${d.version} · ${note}`);
    rememberModels({ kind: "set", source: d.source, version: d.version, note });
  } catch (e) {
    setModelStatus(errText(e) + stale(), true);
  }
}

// ---------- sessions ----------

// activate makes a session the active one, restoring its paths, result and models.
async function activate(id: string) {
  const s = S.sessions.find((x) => x.id === id);
  if (!s) return;
  if (S.session && S.session.id !== id) {
    const prev = S.perSession.get(S.session.id);
    S.perSession.set(S.session.id, { ...prev, paths: pathsArea.value, result: S.result });
  }
  S.session = s;
  store.set("active", id);
  const dev = devices.find((d) => d.session?.id === id);
  if (dev) { selectedKey = dev.key; renderDevices(); }
  const st = S.perSession.get(id);
  pathsArea.value = st?.paths ?? (store.get("paths." + id) || (s.paths ?? []).join("\n"));
  S.result = st?.result ?? null;
  if (S.result) buildData(S.result.data);
  resultMeta.textContent = "";
  renderData();
  connected();
  updateCmd();
  await modelsForSession(s);
  if (markedOnly) await computeMarked();
  refreshTree();
}

function connected() {
  const s = S.session!;
  activeLabel.textContent = `${s.label}${s.label !== s.address ? ` (${s.address})` : ""}${s.hops?.length ? " via " + s.hops.join(" → ") : ""} · gNMI ${s.capabilities.gnmiVersion} · ${s.capabilities.models.length} models · ${s.connectMs} ms`;
  clear(encSel);
  encSel.append(h("option", { value: "" }, `auto (${s.encoding})`),
    ...s.capabilities.encodings.map((e) => h("option", { value: e }, e)));
  renderCaps();
  renderFooter();
}

async function disconnect(id: string) {
  await api.disconnect(id).catch(() => {});
  S.sessions = S.sessions.filter((x) => x.id !== id);
  S.perSession.delete(id);
  for (const d of devices) if (d.session?.id === id) { d.session = undefined; d.state = "off"; }
  if (S.session?.id === id) {
    S.session = null;
    const next = S.sessions[S.sessions.length - 1];
    if (next) { await activate(next.id); return; }
    S.result = null;
    renderData();
    activeLabel.textContent = "No device connected";
    renderCaps();
  }
  renderDevices();
  renderFooter();
  updateCmd();
}

function renderCaps() {
  clear(capsHost);
  const s = S.session;
  if (!s) { capsHost.append(h("p", { class: "muted" }, "Connect to see capabilities.")); return; }
  const filter = h("input", { type: "search", placeholder: "Filter models" });
  const body = h("tbody");
  const draw = () => {
    clear(body);
    const q = filter.value.toLowerCase();
    for (const m of s.capabilities.models) {
      if (q && !`${m.name} ${m.organization} ${m.version}`.toLowerCase().includes(q)) continue;
      body.append(h("tr", {}, h("td", {}, m.name), h("td", {}, m.version ?? ""), h("td", {}, m.organization ?? "")));
    }
  };
  filter.addEventListener("input", draw);
  draw();
  append(capsHost,
    h("p", {}, `gNMI ${s.capabilities.gnmiVersion} · encodings: ${s.capabilities.encodings.join(", ")} · ${s.capabilities.models.length} models`),
    h("div", { class: "row" }, filter,
      h("button", { onclick: () => copyText(stringify(s.capabilities), "capabilities JSON") }, "Copy as JSON")),
    h("table", { class: "grid" }, h("thead", {}, h("tr", {}, h("th", {}, "Model"), h("th", {}, "Version"), h("th", {}, "Organization"))), body));
}

function renderFooter() {
  clear(footer);
  const s = S.session;
  append(footer,
    h("span", { class: s ? "dot ok" : "dot" }), s ? `${s.label} · ${S.sessions.length} open` : "not connected",
    h("span", { class: "sep" }),
    S.schema ? `${S.schema.modules.length} modules · ${S.schema.nodes.toLocaleString()} nodes · loaded in ${S.schema.loadMs} ms` : "no models",
    S.schema?.warnings?.length ? h("button", { class: "link", onclick: () => showWarnings() }, `${S.schema.warnings.length} warning${S.schema.warnings.length === 1 ? "" : "s"}`) : null,
    h("span", { class: "grow" }),
    consoleUI.btn);
}

function showWarnings() {
  modal("Model warnings", h("pre", { class: "warn-list" }, (S.schema?.warnings ?? []).join("\n")), []);
}

// ---------- schema tree ----------

async function loadSchema() {
  const dirs = yangInput.value.split(";").map((s) => s.trim()).filter(Boolean);
  if (!dirs.length) { toast("Enter a YANG folder", "err"); return; }
  store.set("yang", yangInput.value);
  loadBtn.disabled = true;
  schemaInfo.textContent = "Loading models…";
  try {
    schemaLoaded(await api.loadSchema(dirs));
  } catch (e) {
    schemaInfo.textContent = errText(e);
    schemaInfo.className = "err small";
  } finally {
    loadBtn.disabled = false;
  }
}

// schemaFor is the session whose models are loaded (the server has one schema).
let schemaFor = "";

async function schemaLoaded(s: SchemaSummary) {
  schemaFor = S.session?.id ?? "";
  S.schema = s;
  S.nodes.clear(); S.children.clear(); S.expanded.clear(); S.selected = -1;
  schemaInfo.className = "muted small";
  schemaInfo.textContent = `${s.files} files · ${s.modules.length} modules · ${s.nodes.toLocaleString()} nodes · ${s.loadMs} ms`;
  await loadChildren(-1);
  refreshTree();
  renderFooter();
  refreshReport();
}

async function loadChildren(id: number) {
  if (S.children.has(id)) return;
  const kids = await api.children(id);
  for (const k of kids) S.nodes.set(k.id, k);
  S.children.set(id, kids.map((k) => k.id));
}

function visibleRows() {
  const rows: { node: SNode; depth: number }[] = [];
  const walk = (id: number, depth: number, inside: boolean) => {
    for (const c of S.children.get(id) ?? []) {
      if (markedOnly && !inside && !allowedIds.has(c) && !markedIds.has(c)) continue;
      const n = S.nodes.get(c)!;
      rows.push({ node: n, depth });
      if (S.expanded.has(c)) walk(c, depth + 1, inside || markedIds.has(c));
    }
  };
  walk(-1, 0, false);
  return rows;
}

function refreshTree() {
  if (S.searchResults) {
    searchList.setItems(S.searchResults);
    return;
  }
  tree.setItems(visibleRows());
}

function markedPaths(): string[] {
  return pathsArea.value.split("\n").map((s) => s.trim()).filter(Boolean);
}

// originFor returns the gNMI origin a qualified path needs: OpenConfig modules
// live under "openconfig" on most devices (SR Linux, NX-OS, ...).
function originFor(p: string): string {
  const m = /^\/([^/:[\]]+):/.exec(p);
  return m && m[1].startsWith("openconfig-") ? "openconfig:" : "";
}
function stripOrigin(p: string): string { return p.startsWith("/") ? p : p.replace(/^[^/[]*:(?=\/)/, ""); }
function unqualify(p: string): string { return stripOrigin(p).replace(/\/[^/[\]]+:/g, "/"); }
// pathOf is the path used for a schema node (marks, Get, copy).
function pathOf(n: SNode): string { return qualified ? originFor(n.qpath) + n.qpath : n.path; }
// reqPath turns a data path (as the device sent it) into a request path.
function reqPath(p: string): string { return qualified ? originFor(p) + stripOrigin(p) : unqualify(p); }
function isMarked(n: SNode): boolean {
  return markedPaths().some((p) => { const q = stripOrigin(p); return q.includes(":") ? q === n.qpath : q === n.path; });
}

function setPaths(ps: string[]) {
  pathsArea.value = ps.join("\n");
  pathsChanged();
}

function pathsChanged() {
  if (S.session) store.set("paths." + S.session.id, pathsArea.value);
  if (markedOnly) computeMarked().then(refreshTree); else { tree.refresh(); searchList.refresh(); }
  updateCmd();
}

function toggleMark(path: string, on: boolean) {
  const ps = markedPaths().filter((p) => p !== path);
  if (on) ps.push(path);
  setPaths(ps);
}

function toggleNode(n: SNode, on: boolean) {
  const ps = markedPaths().filter((p) => { const q = stripOrigin(p); return !(q.includes(":") ? q === n.qpath : q === n.path); });
  if (on) ps.push(pathOf(n));
  setPaths(ps);
}

// ---------- marked only ----------

let markedOnly = false;
const allowedIds = new Set<number>(); // ancestors of marked nodes
const markedIds = new Set<number>();

async function computeMarked() {
  allowedIds.clear();
  markedIds.clear();
  if (!S.schema) return;
  for (const p of markedPaths()) {
    try {
      const r = await api.find(p);
      markedIds.add(r.node.id);
      for (const a of r.ancestors) { allowedIds.add(a); await loadChildren(a); S.expanded.add(a); }
    } catch { /* not in the models */ }
  }
}

async function setMarkedOnly(on: boolean) {
  markedOnly = on;
  markedOnlyBtn.classList.toggle("on", on);
  if (on) {
    searchInput.value = "";
    S.searchResults = null;
    treeHost.replaceChildren(tree.el);
    await computeMarked();
  }
  refreshTree();
}

function kindBadge(n: SNode) {
  const k = { container: "C", list: "L", leaf: "l", "leaf-list": "ll" }[n.kind] ?? "?";
  return h("span", { class: `kind k-${n.kind}`, title: n.kind }, k);
}

function renderTreeRow(n: SNode, depth: number) {
  const open = S.expanded.has(n.id);
  const row = h("div", { class: "trow" + (S.selected === n.id ? " sel" : "") + (n.config ? "" : " ro") });
  row.style.paddingLeft = `${depth * 16 + 4}px`;
  const caret = h("span", { class: "caret" + (n.hasChildren ? "" : " none") }, n.hasChildren ? (open ? "▾" : "▸") : "");
  caret.addEventListener("click", (e) => { e.stopPropagation(); toggle(n.id); });
  const cb = h("input", { type: "checkbox", title: "Mark this path", checked: isMarked(n) });
  cb.addEventListener("click", (e) => e.stopPropagation());
  cb.addEventListener("change", () => toggleNode(n, cb.checked));
  append(row, caret, cb, kindBadge(n), overlayMark(n), nodeName(n),
    n.keys?.length ? h("span", { class: "keys" }, `[${n.keys.join(" ")}]`) : null,
    n.type ? h("span", { class: "type" }, n.type.name) : null,
    h("span", { class: "rw" }, n.config ? "rw" : "ro"));
  row.addEventListener("click", () => select(n.id));
  row.addEventListener("dblclick", () => n.hasChildren && toggle(n.id));
  return row;
}

// nodeName shows the module where it changes from the parent (roots and
// augmented nodes) when qualified names are on.
function nodeName(n: SNode) {
  const parentMod = n.parent >= 0 ? S.nodes.get(n.parent)?.module : undefined;
  const showMod = qualified && n.module && (n.parent < 0 || (parentMod !== undefined && parentMod !== n.module));
  return h("span", { class: "name", title: n.module }, showMod ? h("span", { class: "mod" }, n.module + ":") : null, n.name);
}

function renderSearchRow(n: SNode) {
  const row = h("div", { class: "srow" + (S.selected === n.id ? " sel" : "") });
  const cb = h("input", { type: "checkbox", title: "Mark this path", checked: isMarked(n) });
  cb.addEventListener("click", (e) => e.stopPropagation());
  cb.addEventListener("change", () => toggleNode(n, cb.checked));
  append(row, h("div", {}, cb, kindBadge(n), h("span", { class: "name" }, n.name), h("span", { class: "muted small" }, ` ${n.module}`)),
    h("div", { class: "spath" }, pathOf(n)));
  row.addEventListener("click", () => reveal(n.id));
  return row;
}

async function toggle(id: number) {
  if (S.expanded.has(id)) {
    S.expanded.delete(id);
  } else {
    await loadChildren(id);
    S.expanded.add(id);
  }
  refreshTree();
}

async function search(q: string) {
  if (!S.schema) return;
  q = q.trim();
  if (q && markedOnly) setMarkedOnly(false);
  if (!q) {
    S.searchResults = null;
    treeHost.replaceChildren(tree.el);
    refreshTree();
    return;
  }
  try {
    S.searchResults = await api.search(q);
    for (const n of S.searchResults) S.nodes.set(n.id, n);
    treeHost.replaceChildren(h("div", { class: "muted small pad" }, `${S.searchResults.length} matches${S.searchResults.length >= 300 ? " (first 300)" : ""}`), searchList.el);
    refreshTree();
  } catch (e) {
    toast(errText(e), "err");
  }
}

// reveal expands the ancestors of id, closes the search and selects the node.
async function reveal(id: number) {
  const d = await api.node(id);
  for (const a of d.ancestors ?? []) {
    await loadChildren(a);
    S.expanded.add(a);
  }
  await loadChildren(d.node.parent);
  searchInput.value = "";
  S.searchResults = null;
  treeHost.replaceChildren(tree.el);
  refreshTree();
  const rows = visibleRows();
  const i = rows.findIndex((r) => r.node.id === id);
  if (i >= 0) tree.scrollToIndex(i);
  showDetails(d);
}

async function select(id: number) {
  try {
    showDetails(await api.node(id));
  } catch (e) {
    toast(errText(e), "err");
  }
}

function typeText(t: TypeInfo): string {
  let s = t.name === t.base ? t.base : `${t.name} (${t.base})`;
  if (t.range) s += ` range ${t.range}`;
  if (t.length) s += ` length ${t.length}`;
  if (t.fractionDigits) s += ` fraction-digits ${t.fractionDigits}`;
  if (t.leafref) s += ` → ${t.leafref}`;
  if (t.identityBase) s += ` base ${t.identityBase}`;
  return s;
}

function showDetails(d: NodeDetail) {
  const n = d.node;
  S.selected = n.id;
  tree.refresh();
  searchList.refresh();
  clear(details);
  const rows: [string, Node | string][] = [
    ["Kind", n.kind + (n.presence ? " (presence)" : "")],
    ["Config", n.config ? "config (read-write)" : "state (read-only)"],
    ["Module", n.module + (n.prefix ? ` (prefix ${n.prefix})` : "")],
  ];
  if (n.keys?.length) rows.push(["Keys", n.keys.join(", ")]);
  if (n.type) rows.push(["Type", typeText(n.type)]);
  if (n.type?.union?.length) rows.push(["Union of", n.type.union.map(typeText).join(" | ")]);
  if (n.type?.enums?.length) rows.push(["Enums", n.type.enums.join(", ")]);
  if (n.type?.bits?.length) rows.push(["Bits", n.type.bits.join(", ")]);
  if (n.type?.patterns?.length) rows.push(["Pattern", n.type.patterns.join("\n")]);
  if (n.units) rows.push(["Units", n.units]);
  if (n.default?.length) rows.push(["Default", n.default.join(", ")]);
  if (n.mandatory) rows.push(["Mandatory", "yes"]);
  if (n.choice) rows.push(["Choice/case", n.choice]);
  if (n.hasChildren) rows.push(["Children", String(n.childCount)]);
  if (n.location) rows.push(["Defined at", n.location]);
  const marked = isMarked(n);
  const np = pathOf(n), other = qualified ? n.path : originFor(n.qpath) + n.qpath;
  const twin = twinPath(n.path);
  const instHost = h("div", { class: "instances-host" });
  append(details,
    h("h2", {}, kindBadge(n), " ", n.name),
    h("div", { class: "path-box" },
      h("code", {}, np),
      h("button", { class: "small", onclick: () => copyText(np, "path") }, "Copy"),
      h("button", { class: "small", title: other, onclick: () => copyText(other, qualified ? "plain path" : "qualified path") }, qualified ? "Copy plain" : "Copy qualified")),
    h("div", { class: "row" },
      h("button", { class: "primary", onclick: () => { if (!isMarked(n)) toggleNode(n, true); showTab("Data"); doGet([np]); } }, "Get this path"),
      h("button", { onclick: () => { toggleNode(n, !marked); showDetails(d); } }, marked ? "Unmark" : "Mark"),
      n.kind === "list" && n.keys?.length ? h("button", { title: "List the entries on the device (reads only the keys)", onclick: () => showInstances(n, instHost) }, "Instances") : null,
      twin ? h("button", {
        title: `Go to ${twin.includes("/state/") ? "the state" : "the config"} twin`,
        onclick: async () => { try { reveal((await api.find(twin)).node.id); } catch { toast("No twin in the loaded models", "err"); } },
      }, twin.includes("/state/") ? "→ state" : "→ config") : null,
      n.config ? h("button", {
        title: "Build a change for this node: validate, preview, then apply with confirmation",
        onclick: () => {
          if (!S.session) { toast("Connect a device first", "err"); return; }
          openConfigDialog(n, S.session, () => { if (markedPaths().length) doGet(); }, { path: np, shell });
        },
      }, "Configure…") : null,
      n.config ? help("configure") : null),
    instHost,
    h("table", { class: "props" }, h("tbody", {}, ...rows.map(([k, v]) => h("tr", {}, h("th", {}, k), h("td", {}, v))))),
    n.description ? h("div", { class: "desc" }, n.description) : null,
    d.source ? h("details", { open: true }, h("summary", {}, "YANG source",
      d.webUrl ? h("a", { class: "src-link", href: d.webUrl, target: "_blank", rel: "noopener noreferrer", title: "This file and line in the model repository on GitHub", onclick: (ev: Event) => ev.stopPropagation() }, "Open on GitHub ↗") : null),
      h("pre", { class: "src" }, d.source)) : null,
    d.sourceError ? h("p", { class: "muted small" }, `Source unavailable: ${d.sourceError}`) : null);
}

// ---------- data ----------

interface DNode {
  id: number; label: string; path: string; depth: number; value: unknown;
  parent: number; kids: number[]; leaf: boolean; listKey?: string;
}
let dnodes: DNode[] = [];
const dataOpen = new Set<number>();
const dataSel = new Set<number>();
const dataList = new VirtualList<DNode>(22, (n) => renderDataRow(n));

const keyGuess = ["name", "id", "index", "prefix", "address", "ip-prefix", "neighbor-address", "identifier", "key"];
const bare = (k: string) => (k.includes(":") ? k.slice(k.indexOf(":") + 1) : k);

function isScalar(v: unknown) { return v === null || typeof v !== "object" || v instanceof Num; }

function buildData(v: unknown) {
  dnodes = [];
  dataOpen.clear();
  dataSel.clear();
  syncUntick();
  const add = (label: string, path: string, value: unknown, depth: number, parent: number, listKey?: string): number => {
    const n: DNode = { id: dnodes.length, label, path, depth, value, parent, kids: [], leaf: isScalar(value) || (Array.isArray(value) && value.every(isScalar)), listKey };
    dnodes.push(n);
    if (!n.leaf) {
      if (Array.isArray(value)) {
        value.forEach((e, i) => {
          let sel = `[${i}]`, key: string | undefined;
          if (e && typeof e === "object" && !(e instanceof Num)) {
            const k = keyGuess.map((g) => Object.keys(e).find((x) => bare(x) === g)).find((x) => x && isScalar((e as any)[x]));
            if (k) { key = k; sel = `[${bare(k)}=${String((e as any)[k])}]`; }
          }
          n.kids.push(add(sel, path + sel, e, depth + 1, n.id, key));
        });
      } else {
        for (const k of Object.keys(value as object)) {
          n.kids.push(add(k, (path === "/" ? "" : path) + "/" + k, (value as any)[k], depth + 1, n.id));
        }
      }
    }
    return n.id;
  };
  add("/", "/", v, 0, -1);
  // open the first levels so something is visible
  for (const n of dnodes) if (n.depth < 3 && !n.leaf) dataOpen.add(n.id);
}

function visibleData(): DNode[] {
  const out: DNode[] = [];
  const walk = (id: number) => {
    const n = dnodes[id];
    out.push(n);
    if (dataOpen.has(id)) for (const k of n.kids) walk(k);
  };
  if (dnodes.length) walk(0);
  return out;
}

function preview(v: unknown): string {
  if (v instanceof Num) return v.raw;
  if (Array.isArray(v)) return v.every(isScalar) ? stringify(v, 0) : `[${v.length}]`;
  if (v && typeof v === "object") return `{${Object.keys(v).length}}`;
  return JSON.stringify(v);
}

function selectedInside(id: number): boolean {
  for (let n: DNode | undefined = dnodes[id]; n; n = n.parent >= 0 ? dnodes[n.parent] : undefined) {
    if (dataSel.has(n.id)) return true;
  }
  return false;
}

function renderDataRow(n: DNode) {
  const row = h("div", { class: "drow" + (selectedInside(n.id) ? " sel" : "") });
  row.style.paddingLeft = `${n.depth * 16 + 4}px`;
  const caret = h("span", { class: "caret" + (n.leaf ? " none" : "") }, n.leaf ? "" : dataOpen.has(n.id) ? "▾" : "▸");
  caret.addEventListener("click", () => { dataOpen.has(n.id) ? dataOpen.delete(n.id) : dataOpen.add(n.id); dataList.setItems(visibleData()); });
  const cb = h("input", { type: "checkbox", checked: dataSel.has(n.id), title: "Select for copying" });
  cb.addEventListener("change", () => { cb.checked ? dataSel.add(n.id) : dataSel.delete(n.id); dataList.refresh(); syncUntick(); });
  const label = qualified ? n.label : bare(n.label);
  append(row, caret, cb, h("span", { class: "dkey" }, label),
    n.leaf ? h("span", { class: "dval" + (typeof n.value === "string" ? " str" : "") }, preview(n.value)) : h("span", { class: "muted" }, preview(n.value)));
  // Full path (with module prefixes) and value on hover; double-click opens the schema node.
  row.title = n.leaf ? `${n.path}\n= ${preview(n.value)}` : n.path;
  row.addEventListener("dblclick", () => locate(n.path));
  return row;
}

// locate shows the schema node for a data path, when models are loaded.
async function locate(path: string) {
  if (!S.schema) return;
  try {
    const r = await api.find(path);
    reveal(r.node.id);
  } catch {
    toast("Path not found in the loaded models", "err");
  }
}

// pick returns the selected parts of the data as a pruned tree from the root.
// List entries keep their key leaf so they stay identifiable.
function pick(id: number): unknown {
  const n = dnodes[id];
  if (dataSel.has(id)) return n.value;
  if (n.leaf) return undefined;
  if (Array.isArray(n.value)) {
    const out: unknown[] = [];
    for (const k of n.kids) {
      const v = pick(k);
      if (v === undefined) continue;
      const kn = dnodes[k];
      if (kn.listKey && v && typeof v === "object" && !Array.isArray(v) && !(kn.listKey in (v as object))) {
        out.push({ [kn.listKey]: (kn.value as any)[kn.listKey], ...(v as object) });
      } else out.push(v);
    }
    return out.length ? out : undefined;
  }
  const out: Record<string, unknown> = {};
  let any = false;
  for (const k of n.kids) {
    const v = pick(k);
    if (v !== undefined) { out[dnodes[k].label] = v; any = true; }
  }
  return any ? out : undefined;
}

async function asText(v: unknown, fmt: "json" | "yaml") {
  if (fmt === "json") return stringify(v) + "\n";
  return (await api.format("yaml", v)).text;
}

async function copySelection(fmt: "json" | "yaml") {
  if (!dataSel.size) { toast("Select data with the checkboxes first", "err"); return; }
  // A single selected node copies exactly that subtree; several are merged into a
  // pruned tree from the root.
  const v = dataSel.size === 1 ? dnodes[[...dataSel][0]].value : pick(0);
  try { copyText(await asText(v, fmt), `selection as ${fmt.toUpperCase()}`); } catch (e) { toast(errText(e), "err"); }
}

// copySmart copies the ticked data, or all of it when nothing is ticked.
function copySmart(fmt: "json" | "yaml") {
  return dataSel.size ? copySelection(fmt) : copyAll(fmt);
}

async function copyAll(fmt: "json" | "yaml") {
  if (!S.result) return;
  try { copyText(await asText(S.result.data, fmt), `all data as ${fmt.toUpperCase()}`); } catch (e) { toast(errText(e), "err"); }
}

function renderViewSel() {
  clear(viewSel);
  for (const v of ["tree", "json", "yaml", "flat"] as const) {
    viewSel.append(h("button", { class: v === dataView ? "active" : "", onclick: () => { dataView = v; renderViewSel(); renderData(); } }, v.toUpperCase()));
  }
}

async function renderData() {
  const r = S.result;
  if (!r) { dataHost.replaceChildren(h("p", { class: "muted" }, "Run a Get to see data.")); return; }
  if (r.empty) { dataHost.replaceChildren(h("p", { class: "muted" }, "The device returned no data for these paths.")); return; }
  switch (dataView) {
    case "tree":
      dataHost.replaceChildren(dataList.el);
      dataList.setItems(visibleData());
      break;
    case "json":
      dataHost.replaceChildren(h("pre", { class: "code" }, stringify(r.data)));
      break;
    case "yaml":
      dataHost.replaceChildren(h("pre", { class: "code" }, "…"));
      try { dataHost.replaceChildren(h("pre", { class: "code" }, (await api.format("yaml", r.data)).text)); }
      catch (e) { dataHost.replaceChildren(h("p", { class: "err" }, errText(e))); }
      break;
    case "flat":
      dataHost.replaceChildren(h("pre", { class: "code" }, r.flat.map((u) => `${u.path} = ${typeof u.value === "string" ? JSON.stringify(u.value) : stringify(u.value, 0)}`).join("\n")));
      break;
  }
}

async function doGet(paths = markedPaths()) {
  if (!S.session) { toast("Connect first", "err"); return; }
  if (!paths.length) { toast("Mark a node or enter a path", "err"); return; }
  getBtn.disabled = true;
  resultMeta.className = "muted small";
  resultMeta.textContent = `Reading ${paths.length} path(s)…`;
  try {
    const r = await api.get(S.session.id, paths, typeSel.value, encSel.value);
    S.result = r;
    recordOverlay(paths, r);
    buildData(r.data);
    resultMeta.textContent = `${r.type} · ${r.encoding} · ${r.updateCount} updates · ${r.leafCount} leaves · ${(r.sizeBytes / 1024).toFixed(1)} KiB · ${r.elapsedMs} ms`;
    renderData();
  } catch (e) {
    S.result = null;
    recordOverlay(paths, null, e);
    resultMeta.className = "err small";
    resultMeta.textContent = errText(e);
    dataHost.replaceChildren();
  } finally {
    getBtn.disabled = false;
  }
}

// ---------- command preview ----------

let cmdTimer = 0;
function updateCmd() {
  clearTimeout(cmdTimer);
  cmdTimer = window.setTimeout(genCmd, 200);
}

async function genCmd() {
  const mode = cmdMode.value;
  cmdInterval.hidden = mode !== "stream";
  const paths = markedPaths();
  const req: Record<string, unknown> = {
    paths, mode, shell: shell(), sampleInterval: cmdInterval.value,
    type: typeSel.value, encoding: encSel.value || "",
  };
  if (S.session) req.session = S.session.id;
  else {
    const d = devices.find((x) => x.key === selectedKey);
    if (!d || !d.target.address) { cmdText.textContent = ""; cmdNote.textContent = "Select or connect a device to see the gNMIc command."; return; }
    req.target = d.target; // no password: commands use $GNMI_PASSWORD
  }
  if (mode !== "capabilities" && !paths.length) { cmdText.textContent = ""; cmdNote.textContent = "Mark paths to see the gNMIc command."; return; }
  try {
    const out = await api.generate(req);
    const c = out.commands[0];
    cmdText.textContent = c?.text ?? "";
    cmdNote.textContent = (c?.note ? c.note + ". " : "") + "Credentials come from $GNMI_USERNAME and $GNMI_PASSWORD.";
  } catch (e) {
    cmdText.textContent = "";
    cmdNote.textContent = errText(e);
  }
}

// ---------- restore after reload ----------

async function restoreSessions() {
  let ss: Session[];
  try { ss = await api.sessions(); } catch { return; }
  if (!ss?.length) return;
  for (const s of ss) {
    if (S.sessions.some((x) => x.id === s.id)) continue;
    S.sessions.push(s);
    const key = s.device ? `k:${s.device.namespace}/${s.device.name}` : s.preset ? "p:" + s.preset : "";
    let d = key ? devices.find((x) => x.key === key) : devices.find((x) => !x.session && x.key.startsWith("a:") && x.target.address === s.address);
    if (!d) {
      d = {
        key: key || `a:${++adhocSeq}`, name: s.device ? `${s.device.namespace}/${s.device.name}` : s.label,
        target: { address: s.address, jumps: s.jumps }, state: "off",
        k8s: s.device ? { namespace: s.device.namespace, name: s.device.name, ready: "?", paused: false, provider: s.device.provider } : undefined,
      };
      devices.push(d);
    }
    d.session = s;
    d.state = "on";
  }
  renderDevices();
  const want = store.get("active");
  const s = ss.find((x) => x.id === want) ?? ss[ss.length - 1];
  await activate(s.id);
}

mount();
renderData();

// ---------- match report (advertised vs loaded models) ----------

let lastReport: MatchReport | null = null;

async function refreshReport() {
  clear(reportLine);
  lastReport = null;
  if (!S.session || !S.schema) return;
  try {
    const r = await reportApi.get(S.session.id);
    lastReport = r;
    const c = (k: string) => r.counts[k] ?? 0;
    const bad = c("mismatch") + c("missing");
    reportLine.append(h("button", {
      class: "link " + (bad ? "warn" : "ok"), title: "Compare the models the device advertises with the loaded models",
      onclick: () => showReport(),
    }, `${c("match")} match · ${c("mismatch")} mismatch · ${c("missing")} missing${c("loaded") ? ` · ${c("loaded")} unversioned` : ""}`));
  } catch { /* no session or schema */ }
}

function showReport() {
  const r = lastReport;
  if (!r) return;
  const filter = h("input", { type: "search", placeholder: "Filter modules" });
  const statusSel = h("select", {}, h("option", { value: "" }, "all advertised"),
    ...["mismatch", "missing", "loaded", "match", "extra"].map((s) => h("option", { value: s }, `${s} (${r.counts[s] ?? 0})`)));
  const body = h("tbody");
  const draw = () => {
    clear(body);
    const q = filter.value.toLowerCase();
    for (const e of r.entries) {
      if (statusSel.value ? e.status !== statusSel.value : e.status === "extra") continue;
      if (q && !e.module.toLowerCase().includes(q)) continue;
      body.append(h("tr", {}, h("td", {}, h("span", { class: `st st-${e.status}` }, e.status)), h("td", {}, e.module),
        h("td", {}, e.advertised ?? ""), h("td", {}, e.loaded ?? "")));
    }
  };
  filter.addEventListener("input", draw);
  statusSel.addEventListener("change", draw);
  draw();
  modal("Model match report",
    h("div", { class: "report" },
      h("p", { class: "muted small" }, "Each model the device advertises in Capabilities, compared with the loaded modules (revision date, or OpenConfig version). \"extra\" lists loaded modules the device doesn't advertise, such as imports."),
      h("div", { class: "row" }, filter, statusSel,
        h("button", { class: "small", onclick: () => copyText(stringify(r), "match report JSON") }, "Copy JSON")),
      h("div", { class: "report-table" }, h("table", { class: "grid" },
        h("thead", {}, h("tr", {}, h("th", {}, "Status"), h("th", {}, "Module"), h("th", {}, "Advertised"), h("th", {}, "Loaded"))), body))),
    []);
}

// ---------- data overlay (M3) ----------

// Per session: schema paths (no keys, no prefixes) seen with data, and paths
// whose Get failed (with the gRPC code). Shown as markers in the tree.
const overlays = new Map<string, { data: Set<string>; err: Map<string, string>; empty: Set<string> }>();

function overlay() {
  if (!S.session) return null;
  let o = overlays.get(S.session.id);
  if (!o) { o = { data: new Set(), err: new Map(), empty: new Set() }; overlays.set(S.session.id, o); }
  return o;
}

// schemaKey reduces a data or schema path to "module|/a/b": element names
// without keys or module prefixes, plus the module of the first element when the
// path names one (device data does, as in "/srl_nokia-system:system/..."). The
// module tells apart same-named top nodes such as ietf, openconfig and vendor
// "system".
function schemaKey(p: string): string {
  p = stripOrigin(p);
  const out: string[] = [];
  let depth = 0, cur = "";
  for (const ch of p) {
    if (ch === "[") { depth++; continue; }
    if (ch === "]") { depth--; continue; }
    if (depth) continue;
    if (ch === "/") { if (cur) out.push(cur); cur = ""; continue; }
    cur += ch;
  }
  if (cur) out.push(cur);
  const mod = out[0]?.includes(":") ? out[0].slice(0, out[0].indexOf(":")) : "";
  return mod + "|/" + out.map((s) => (s.includes(":") ? s.slice(s.indexOf(":") + 1) : s)).join("/");
}

function parentKey(k: string): string {
  const [mod, rest] = k.split("|");
  const i = rest.lastIndexOf("/");
  return i <= 0 ? "" : `${mod}|${rest.slice(0, i)}`;
}

function recordOverlay(paths: string[], r: GetResult | null, err?: unknown) {
  const o = overlay();
  if (!o) return;
  if (err) {
    const code = err instanceof ApiError ? (err.err.grpcCode ?? err.err.code) : "error";
    for (const p of paths) o.err.set(schemaKey(p), code);
  } else if (r) {
    for (const p of paths) { o.err.delete(schemaKey(p)); if (r.empty) o.empty.add(schemaKey(p)); }
    for (const u of r.flat) {
      for (let k = schemaKey(u.path); k && !o.data.has(k); k = parentKey(k)) o.data.add(k);
    }
  }
  tree.refresh();
  searchList.refresh();
}

function overlayMark(n: SNode): HTMLElement | null {
  const o = S.session ? overlays.get(S.session.id) : undefined;
  if (!o) return null;
  // The node's top module (from its qualified path) and the module-less key both
  // match: requests the user typed carry no module.
  const withMod = schemaKey(n.qpath);
  const bare = "|" + withMod.split("|")[1];
  const has = (s: Set<string> | Map<string, string>) => s.has(withMod) || s.has(bare);
  const e = o.err.get(withMod) ?? o.err.get(bare);
  if (e) return h("span", { class: "ov err", title: `Get failed here: ${e}` }, "●");
  if (has(o.data)) return h("span", { class: "ov data", title: "The device returned data here" }, "●");
  if (has(o.empty)) return h("span", { class: "ov empty", title: "Read, but the device returned no data" }, "○");
  return null;
}

// ---------- instances view (M3) ----------

// showInstances lists the concrete entries of a list by reading only its key
// leaves, so it stays light even for large lists.
async function showInstances(n: SNode, host: HTMLElement) {
  if (!S.session) { toast("Connect first", "err"); return; }
  clear(host);
  host.append(h("p", { class: "muted small" }, "Reading instances…"));
  const keys = n.keys ?? [];
  const paths = keys.map((k) => `${pathOf(n)}/${k}`);
  try {
    const r = await api.get(S.session.id, paths, "all", "");
    recordOverlay(paths, r);
    const inst = new Map<string, Record<string, string>>();
    for (const u of r.flat) {
      const i = u.path.lastIndexOf("/");
      const base = u.path.slice(0, i);
      const key = u.path.slice(i + 1).replace(/^.*:/, "");
      const rec = inst.get(base) ?? {};
      rec[key] = typeof u.value === "string" ? u.value : stringify(u.value, 0);
      inst.set(base, rec);
    }
    clear(host);
    if (!inst.size) { host.append(h("p", { class: "muted small" }, "No instances on the device.")); return; }
    const filter = h("input", { type: "search", placeholder: `Filter ${inst.size} instances` });
    const body = h("tbody");
    const draw = () => {
      clear(body);
      const q = filter.value.toLowerCase();
      for (const [path, rec] of inst) {
        const label = keys.map((k) => rec[k] ?? "").join(" · ");
        if (q && !label.toLowerCase().includes(q)) continue;
        const display = reqPath(path);
        body.append(h("tr", {},
          h("td", {}, label),
          h("td", { class: "actions" },
            h("button", { class: "small", title: "Read this instance", onclick: () => { showTab("Data"); doGet([display]); } }, "Get"),
            h("button", { class: "small", title: "Add to the paths to read", onclick: () => toggleMark(display, true) }, "Mark"),
            h("button", { class: "small", title: display, onclick: () => copyText(display, "path") }, "Copy path"))));
      }
    };
    filter.addEventListener("input", draw);
    draw();
    append(host, h("div", { class: "row" }, filter),
      h("table", { class: "grid instances" }, h("thead", {}, h("tr", {}, h("th", {}, keys.join(" · ")), h("th", {}, ""))), body));
  } catch (e) {
    recordOverlay(paths, null, e);
    clear(host);
    host.append(h("p", { class: "err small" }, errText(e)));
  }
}

// twinPath returns the OpenConfig config/state sibling of a path, if any.
function twinPath(p: string): string | null {
  if (p.includes("/config/")) return p.replace(/\/config\/(?!.*\/config\/)/, "/state/");
  if (p.includes("/state/")) return p.replace(/\/state\/(?!.*\/state\/)/, "/config/");
  return null;
}
