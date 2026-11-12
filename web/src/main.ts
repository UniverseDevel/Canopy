import { api, ApiError, GetResult, MatchReport, modelsApi, ModelSource, reportApi, NodeDetail, Preset, SchemaSummary, Session, SNode, Target, TypeInfo } from "./api";
import { $, append, clear, copyText, h, toast } from "./dom";
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

function field(label: string, input: HTMLElement, hint?: string) {
  return h("label", { class: "field" }, h("span", {}, label), input, hint ? h("small", {}, hint) : null);
}

const deviceList = h("div", { class: "devices" });
// Full-height device column on the far left, like vertical tabs.
const devicePanel = h("aside", { class: "device-panel" },
  h("div", { class: "brand-row" }, h("div", { class: "brand" }, "Canopy"), h("div", { class: "muted small", id: "build" })),
  h("button", { class: "primary connect-new", onclick: () => openDeviceDialog() }, "+ Connect device"),
  h("div", { class: "muted small section-label" }, "Devices"),
  deviceList);
const activeLabel = h("div", { class: "muted small active-label" }, "No device connected");
let modelSources: ModelSource[] = [];
const modelSrcSel = h("select", { title: "Model source" }, h("option", { value: "" }, "Model source…"));
const modelVerSel = h("select", { title: "Version" }, h("option", { value: "" }, "version"));
const modelLoadBtn = h("button", { class: "small", onclick: () => loadModelSet() }, "Load");
const modelStatus = h("div", { class: "muted small model-status" });
const reportLine = h("div", { class: "small report-line" });

const yangInput = h("input", { placeholder: "YANG folder(s), separated by ;", value: store.get("yang") });
const loadBtn = h("button", { onclick: () => loadSchema() }, "Load");
const searchInput = h("input", { type: "search", placeholder: "Search nodes (name, or path with /)" });
const schemaInfo = h("div", { class: "muted small" }, "No models loaded");
const tree = new VirtualList<{ node: SNode; depth: number }>(24, (r) => renderTreeRow(r.node, r.depth));
const searchList = new VirtualList<SNode>(40, (n) => renderSearchRow(n));
const treeHost = h("div", { class: "tree-host" }, tree.el);

const details = h("div", { class: "details" }, h("p", { class: "muted" }, "Select a node in the tree."));

const pathsArea = h("textarea", { rows: 4, placeholder: "Paths to read, one per line. Mark nodes in the tree to add them." });
const typeSel = h("select", {}, ...["all", "config", "state", "operational"].map((t) => h("option", { value: t }, t)));
const encSel = h("select", {}, h("option", { value: "" }, "encoding: auto"));
const getBtn = h("button", { class: "primary", onclick: () => doGet() }, "Get");
const resultMeta = h("div", { class: "muted small" });
const viewSel = h("div", { class: "seg" });
const dataHost = h("div", { class: "data-host" });
let showPrefixes = store.get("prefixes") === "1";
const prefixChk = h("input", { type: "checkbox", checked: showPrefixes });
prefixChk.addEventListener("change", () => {
  showPrefixes = prefixChk.checked;
  store.set("prefixes", showPrefixes ? "1" : "0");
  dataList.refresh();
});

function expandData(open: boolean) {
  dataOpen.clear();
  if (open) for (const n of dnodes) if (!n.leaf) dataOpen.add(n.id);
  else if (dnodes.length) dataOpen.add(0);
  dataList.setItems(visibleData());
}
let dataView: "tree" | "json" | "yaml" | "flat" = "tree";

const genMode = h("select", {}, ...["get", "once", "stream", "poll", "capabilities"].map((t) => h("option", { value: t }, t)));
const genShell = h("select", {}, h("option", { value: "bash" }, "bash / zsh"), h("option", { value: "powershell" }, "PowerShell"));
const genInterval = h("input", { value: "10s", size: 6, title: "sample interval for stream mode" });
const genOut = h("div", { class: "gen-out" });

const capsHost = h("div", { class: "caps" }, h("p", { class: "muted" }, "Connect to see capabilities."));
const footer = h("footer", {});

const tabs = ["Data", "Commands", "Capabilities"] as const;
type Tab = typeof tabs[number];
const tabBar = h("div", { class: "tabs" });
const tabBodies: Record<Tab, HTMLElement> = {
  Data: h("div", { class: "tab-body" },
    h("div", { class: "get-form" }, pathsArea,
      h("div", { class: "row" }, typeSel, encSel, getBtn,
        h("button", { onclick: () => { pathsArea.value = ""; refreshTree(); } }, "Clear paths"))),
    h("div", { class: "row between" }, viewSel, resultMeta),
    h("div", { class: "row" },
      h("button", { class: "small", onclick: () => expandData(true) }, "Expand all"),
      h("button", { class: "small", onclick: () => expandData(false) }, "Collapse all"),
      h("label", { class: "check small", title: "Show module prefixes such as srl_nokia-interfaces: in names (display only; copies keep them)" }, prefixChk, "Module prefixes")),
    h("div", { class: "row" },
      h("button", { onclick: () => copySelection("json") }, "Copy selection as JSON"),
      h("button", { onclick: () => copySelection("yaml") }, "Copy selection as YAML"),
      h("button", { onclick: () => copyAll("json") }, "Copy all JSON"),
      h("button", { onclick: () => copyAll("yaml") }, "Copy all YAML"),
      h("button", { onclick: () => { dataSel.clear(); dataList.refresh(); } }, "Clear selection")),
    dataHost),
  Commands: h("div", { class: "tab-body" },
    h("p", { class: "muted small" }, "Commands use the paths from the Data tab and the current connection. Credentials come from $GNMI_USERNAME and $GNMI_PASSWORD; nothing secret is embedded."),
    h("div", { class: "row" }, field("Mode", genMode), field("Shell", genShell), field("Sample interval", genInterval),
      h("button", { class: "primary", onclick: () => generate() }, "Generate")),
    genOut),
  Capabilities: h("div", { class: "tab-body" }, capsHost),
};
let activeTab: Tab = "Data";

function showTab(t: Tab) {
  activeTab = t;
  clear(tabBar);
  for (const x of tabs) {
    tabBar.append(h("button", { class: x === t ? "tab active" : "tab", onclick: () => showTab(x) }, x));
  }
  for (const x of tabs) tabBodies[x].hidden = x !== t;
}

// ---------- layout: resizable panes, collapsible connection form ----------

const leftPane = h("section", { class: "pane left" },
  h("div", { class: "models-panel" },
    h("div", { class: "row" }, h("strong", {}, "Models"), modelSrcSel, modelVerSel, modelLoadBtn),
    modelStatus, reportLine,
    h("details", { class: "local-models" }, h("summary", { class: "muted small" }, "Local folder instead"),
      h("div", { class: "row" }, yangInput, loadBtn))),
  schemaInfo, searchInput, treeHost);
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
      footer));
  setupSplitters();
  showTab("Data");
  renderViewSel();
  let searchTimer = 0;
  searchInput.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = window.setTimeout(() => search(searchInput.value), 200);
  });
  pathsArea.addEventListener("input", () => tree.refresh());
  yangInput.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Enter") loadSchema(); });
  api.info().then((i) => ($("#build").textContent = `${i.build.version} ${i.build.commit.slice(0, 8)}`)).catch(() => {});
  api.schema().then((s) => { if (s) schemaLoaded(s); }).catch(() => {});
  initModels();
  loadDevices();
  renderFooter();
}


// ---------- modal ----------

function modal(title: string, body: HTMLElement, buttons: { label: string; primary?: boolean; value: string }[]): Promise<string | null> {
  return new Promise((resolve) => {
    const close = (v: string | null) => { back.remove(); resolve(v); };
    const back = h("div", { class: "modal-back" },
      h("div", { class: "modal" }, h("h3", {}, title), body,
        h("div", { class: "row end" }, ...buttons.map((b) =>
          h("button", { class: b.primary ? "primary" : "", onclick: () => close(b.value) }, b.label)),
          h("button", { onclick: () => close(null) }, "Cancel"))));
    back.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Escape") close(null); });
    document.body.append(back);
    (back.querySelector("input") ?? back.querySelector("button.primary") as HTMLElement | null)?.focus();
  });
}

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
  if (!devices.length) {
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
    const edit = h("button", {
      class: "small icon", title: d.preset?.readOnly ? "Copy into a new device" : "Edit",
      onclick: (e: Event) => { e.stopPropagation(); openDeviceDialog(d); },
    }, "✎");
    const sub = d.target.address + (d.target.jumps?.length ? ` via ${d.target.jumps.join(", ")}` : "");
    const row = h("div", { class: "device" + (d.key === selectedKey ? " sel" : "") + (on ? " on" : "") },
      h("span", { class: "dot " + d.state, title: { off: "not connected", busy: "connecting", on: "connected", err: "error" }[d.state] }),
      h("div", { class: "dev-main" },
        h("div", { class: "dev-name" }, d.name, d.preset?.readOnly ? h("span", { class: "tag" }, "lab/team") : null,
          !d.preset ? h("span", { class: "tag" }, "not saved") : null),
        h("div", { class: "muted small dev-sub" }, sub),
        d.state === "err" && d.error ? h("div", { class: "err small dev-sub", title: d.error }, d.error) : null),
      edit, btn);
    row.title = [d.description, d.preset?.lastSeen ? `last connected ${new Date(d.preset.lastSeen.time).toLocaleString()}` : ""].filter(Boolean).join("\n");
    row.addEventListener("click", () => selectDevice(d));
    row.addEventListener("dblclick", () => { if (d.state === "off" || d.state === "err") connectDevice(d); });
    deviceList.append(row);
  }
}

async function selectDevice(d: Device) {
  selectedKey = d.key;
  renderDevices();
  if (d.session && S.session?.id !== d.session.id) await activate(d.session.id);
}

function passwordPrompt(title: string, note: string): Promise<string | null> {
  const inp = h("input", { type: "password", placeholder: "password", autocomplete: "current-password" });
  return new Promise((res) => {
    inp.addEventListener("keydown", (ev: KeyboardEvent) => {
      if (ev.key === "Enter") { (inp.closest(".modal-back") as HTMLElement).remove(); res(inp.value); }
    });
    modal(title, h("div", { class: "form" }, h("p", { class: "muted small" }, note), inp), [{ label: "OK", primary: true, value: "ok" }])
      .then((r) => res(r === "ok" ? inp.value : null));
  });
}

async function connectDevice(d: Device) {
  d.state = "busy";
  d.error = "";
  renderDevices();
  const t: Target = { ...d.target, password: d.password };
  try {
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        const s = await api.connect(t);
        d.session = s;
        d.state = "on";
        S.sessions.push(s);
        selectedKey = d.key;
        renderDevices();
        await activate(s.id);
        if (d.preset && !d.preset.readOnly) loadDevices(); // refresh "last connected"
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
          t.passphrases = { ...(t.passphrases ?? {}), [x.keyFile]: v };
          continue;
        }
        if (e.err.code === "auth_failed") {
          const v = await passwordPrompt(`Password for ${t.username || "the device"} on ${d.name}`,
            t.password ? "The password was not accepted. Try again (kept in memory only)." :
              "No password was set for this device (or the one from the environment was not accepted). It is kept in memory only.");
          if (v === null) throw e;
          t.password = v;
          d.password = v;
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

// openDeviceDialog adds a device, or edits one (copy for read-only presets).
async function openDeviceDialog(edit?: Device) {
  const p = edit?.preset;
  const t = edit?.target;
  const v = (x?: string) => x ?? "";
  const name = h("input", { value: edit ? (p?.readOnly ? `${edit.name}-copy` : edit.name) : "", placeholder: "e.g. leaf1 (optional unless saved)" });
  const address = h("input", { value: v(t?.address), placeholder: "host:port, e.g. 10.0.0.5:57400" });
  const tls = h("select", {},
    h("option", { value: "verify" }, "TLS, verify certificate"),
    h("option", { value: "skip" }, "TLS, skip verification"),
    h("option", { value: "plain" }, "Plaintext (insecure)"));
  tls.value = t ? (t.insecure ? "plain" : t.skipVerify ? "skip" : "verify") : "skip";
  const ca = h("input", { value: v(t?.caFile), placeholder: "CA file (optional)" });
  const serverName = h("input", { value: v(t?.serverName), placeholder: "server name override (optional)" });
  const username = h("input", { value: v(t?.username), placeholder: "username", autocomplete: "username" });
  const password = h("input", { type: "password", value: v(edit?.password), placeholder: "kept in memory only", autocomplete: "current-password" });
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
  const save = h("input", { type: "checkbox", checked: !!p && !p.readOnly });
  const pwEnv = h("input", { value: v(p?.passwordEnv), placeholder: "GNMI_PASSWORD" });
  const saveFields = h("div", { class: "form-grid" },
    field("Description", desc), field("Password environment variable", pwEnv, "Used when no password is typed. Passwords are never saved."));
  const syncSave = () => { saveFields.hidden = !save.checked; };
  save.addEventListener("change", syncSave);
  syncSave();
  const body = h("div", { class: "form" },
    h("div", { class: "form-grid" },
      field("Name", name), field("Target", address), field("Security", tls), field("Timeout (s)", timeout),
      field("CA file", ca), field("TLS server name", serverName), field("Username", username), field("Password", password),
      field("SSH jump hosts", jumps, "comma-separated chain"), field("SSH key file", identity),
      field("YANG models", modelSrc), field("Model version", modelVer)),
    h("label", { class: "check" }, save, " Save this device (passwords are never saved)"),
    saveFields);
  const buttons = [{ label: "Connect", primary: true, value: "connect" }, { label: "Add to list", value: "add" }];
  const removable = edit && (!edit.preset || !edit.preset.readOnly);
  if (removable) buttons.push({ label: edit!.preset ? "Delete saved device" : "Remove", value: "remove" });
  const title = !edit ? "Connect device" : p?.readOnly ? `New device from “${edit.name}”` : `Edit “${edit.name}”`;
  for (;;) {
    const r = await modal(title, body, buttons);
    if (r === null) return;
    if (r === "remove") {
      if (edit!.session) await disconnectDevice(edit!);
      if (edit!.preset) {
        try { await api.deletePreset(edit!.preset.name); } catch (e) { toast(errText(e), "err"); return; }
      }
      devices.splice(devices.indexOf(edit!), 1);
      await loadDevices();
      return;
    }
    if (!address.value.trim()) { toast("Enter a target address", "err"); continue; }
    if (save.checked && !name.value.trim()) { toast("A saved device needs a name", "err"); continue; }
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
    if (save.checked) {
      const preset: Preset = {
        name: name.value.trim(), description: desc.value.trim() || undefined, passwordEnv: pwEnv.value.trim() || undefined,
        address: tgt.address, insecure: tgt.insecure, skipVerify: tgt.skipVerify, caFile: tgt.caFile, serverName: tgt.serverName,
        username: tgt.username, timeoutSec: tgt.timeoutSec, jumps: tgt.jumps, identityFiles: tgt.identityFiles,
        models, paths: edit?.paths, yangDirs: edit?.yangDirs, tags: p?.tags,
      };
      try {
        const saved = await api.savePreset(preset);
        if (p && !p.readOnly && p.name !== saved.name) await api.deletePreset(p.name).catch(() => {}); // renamed
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
    if (r === "connect") await connectDevice(d);
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

async function fillModelVersions(select?: string) {
  clear(modelVerSel);
  if (!modelSrcSel.value) { modelVerSel.append(h("option", { value: "" }, "version")); return; }
  modelVerSel.append(h("option", { value: "" }, "loading…"));
  try {
    const vs = await modelsApi.versions(modelSrcSel.value);
    clear(modelVerSel);
    modelVerSel.append(...vs.map((x) => h("option", { value: x.name, title: x.cached ? "downloaded" : "" }, x.name + (x.cached ? "  ✓" : ""))));
    if (select) modelVerSel.value = select;
  } catch (e) {
    clear(modelVerSel);
    modelVerSel.append(h("option", { value: "" }, "listing failed"));
    setModelStatus(errText(e), true);
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
  try {
    const r = await modelsApi.auto(s.id);
    const d = r.detection;
    if (r.error) { setModelStatus(errText(new ApiError(r.error, 0)), true); return; }
    if (!d?.source) { setModelStatus(d?.reason ?? "Platform not recognized; pick models below", true); return; }
    if (!d.version) {
      await selectModelUI(d.source, "");
      setModelStatus(`${d.reason}`, true);
      return;
    }
    const note = d.exact ? `detected (device ${d.deviceVersion ?? ""})`.replace(" ()", "") : `nearest to ${d.want} (device ${d.deviceVersion ?? "?"})`;
    await selectModelUI(d.source, d.version);
    if (r.schema) await schemaLoaded(r.schema);
    const name = modelSources.find((x) => x.id === d.source)?.name ?? d.source;
    setModelStatus(`${name} ${d.version} · ${note}`);
    rememberModels({ kind: "set", source: d.source, version: d.version, note });
  } catch (e) {
    setModelStatus(errText(e), true);
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
  const dev = devices.find((d) => d.session?.id === id);
  if (dev) { selectedKey = dev.key; renderDevices(); }
  const st = S.perSession.get(id);
  pathsArea.value = st?.paths ?? (s.paths ?? []).join("\n");
  S.result = st?.result ?? null;
  if (S.result) buildData(S.result.data);
  resultMeta.textContent = "";
  renderData();
  connected();
  await modelsForSession(s);
  refreshTree();
}

function connected() {
  const s = S.session!;
  activeLabel.textContent = `${s.label}${s.label !== s.address ? ` (${s.address})` : ""}${s.hops?.length ? " via " + s.hops.join(" → ") : ""} · gNMI ${s.capabilities.gnmiVersion} · ${s.capabilities.models.length} models · ${s.connectMs} ms`;
  clear(encSel);
  encSel.append(h("option", { value: "" }, `encoding: auto (${s.encoding})`),
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
    S.schema?.warnings?.length ? h("button", { class: "link", onclick: () => showWarnings() }, `${S.schema.warnings.length} warnings`) : null);
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

async function schemaLoaded(s: SchemaSummary) {
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
  const walk = (id: number, depth: number) => {
    for (const c of S.children.get(id) ?? []) {
      const n = S.nodes.get(c)!;
      rows.push({ node: n, depth });
      if (S.expanded.has(c)) walk(c, depth + 1);
    }
  };
  walk(-1, 0);
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

function toggleMark(path: string, on: boolean) {
  const ps = markedPaths().filter((p) => p !== path);
  if (on) ps.push(path);
  pathsArea.value = ps.join("\n");
  tree.refresh();
  searchList.refresh();
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
  const cb = h("input", { type: "checkbox", title: "Mark this path", checked: markedPaths().includes(n.path) });
  cb.addEventListener("click", (e) => e.stopPropagation());
  cb.addEventListener("change", () => toggleMark(n.path, cb.checked));
  append(row, caret, cb, kindBadge(n), overlayMark(n), h("span", { class: "name" }, n.name),
    n.keys?.length ? h("span", { class: "keys" }, `[${n.keys.join(" ")}]`) : null,
    n.type ? h("span", { class: "type" }, n.type.name) : null,
    h("span", { class: "rw" }, n.config ? "rw" : "ro"));
  row.addEventListener("click", () => select(n.id));
  row.addEventListener("dblclick", () => n.hasChildren && toggle(n.id));
  return row;
}

function renderSearchRow(n: SNode) {
  const row = h("div", { class: "srow" + (S.selected === n.id ? " sel" : "") });
  const cb = h("input", { type: "checkbox", title: "Mark this path", checked: markedPaths().includes(n.path) });
  cb.addEventListener("click", (e) => e.stopPropagation());
  cb.addEventListener("change", () => toggleMark(n.path, cb.checked));
  append(row, h("div", {}, cb, kindBadge(n), h("span", { class: "name" }, n.name), h("span", { class: "muted small" }, ` ${n.module}`)),
    h("div", { class: "spath" }, n.path));
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
  const marked = markedPaths().includes(n.path);
  const twin = twinPath(n.path);
  const instHost = h("div", { class: "instances-host" });
  append(details,
    h("h2", {}, kindBadge(n), " ", n.name),
    h("div", { class: "path-box" },
      h("code", {}, n.path),
      h("button", { class: "small", onclick: () => copyText(n.path, "path") }, "Copy"),
      h("button", { class: "small", onclick: () => copyText(n.qpath, "module-qualified path") }, "Copy qualified")),
    h("div", { class: "row" },
      h("button", { class: "primary", onclick: () => { if (!markedPaths().includes(n.path)) toggleMark(n.path, true); showTab("Data"); doGet([n.path]); } }, "Get this path"),
      h("button", { onclick: () => { toggleMark(n.path, !marked); showDetails(d); } }, marked ? "Unmark" : "Mark"),
      n.kind === "list" && n.keys?.length ? h("button", { title: "List the entries on the device (reads only the keys)", onclick: () => showInstances(n, instHost) }, "Instances") : null,
      twin ? h("button", {
        title: `Go to ${twin.includes("/state/") ? "the state" : "the config"} twin`,
        onclick: async () => { try { reveal((await api.find(twin)).node.id); } catch { toast("No twin in the loaded models", "err"); } },
      }, twin.includes("/state/") ? "→ state" : "→ config") : null),
    instHost,
    h("table", { class: "props" }, h("tbody", {}, ...rows.map(([k, v]) => h("tr", {}, h("th", {}, k), h("td", {}, v))))),
    n.description ? h("div", { class: "desc" }, n.description) : null,
    d.source ? h("details", { open: true }, h("summary", {}, "YANG source"), h("pre", { class: "src" }, d.source)) : null,
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
  cb.addEventListener("change", () => { cb.checked ? dataSel.add(n.id) : dataSel.delete(n.id); dataList.refresh(); });
  const label = showPrefixes ? n.label : bare(n.label);
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

// ---------- commands ----------

async function generate() {
  const paths = markedPaths();
  const req: Record<string, unknown> = {
    paths, mode: genMode.value, shell: genShell.value, sampleInterval: genInterval.value,
    type: typeSel.value, encoding: encSel.value || S.session?.encoding || "",
  };
  if (S.session) req.session = S.session.id;
  else {
    const d = devices.find((x) => x.key === selectedKey);
    if (!d) { toast("Select or connect a device first", "err"); return; }
    req.target = d.target; // no password: generated commands use $GNMI_PASSWORD
  }
  clear(genOut);
  try {
    const out = await api.generate(req);
    for (const c of out.commands) {
      genOut.append(h("div", { class: "cmd" },
        h("div", { class: "row between" }, h("strong", {}, c.title), h("button", { class: "small", onclick: () => copyText(c.text, "command") }, "Copy")),
        c.note ? h("div", { class: "muted small" }, c.note) : null,
        h("pre", { class: "code" }, c.text)));
    }
    if (out.config) {
      genOut.append(h("div", { class: "cmd" },
        h("div", { class: "row between" }, h("strong", {}, "gNMIc config file"), h("button", { class: "small", onclick: () => copyText(out.config!, "config") }, "Copy")),
        h("pre", { class: "code" }, out.config)));
    }
  } catch (e) {
    genOut.append(h("p", { class: "err" }, errText(e)));
  }
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
  const paths = keys.map((k) => `${n.path}/${k}`);
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
        const display = showPrefixes ? path : path.replace(/\/[^/[\]]+:/g, "/");
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
