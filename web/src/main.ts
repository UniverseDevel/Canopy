import { api, ApiError, GetResult, NodeDetail, Preset, SchemaSummary, Session, SNode, Target, TypeInfo } from "./api";
import { $, append, clear, copyText, h, toast } from "./dom";
import { Num, stringify } from "./json";
import { VirtualList } from "./vlist";

// ---------- state ----------

const S = {
  session: null as Session | null, // the active session
  sessions: [] as Session[],
  // per-session working state, restored when switching devices
  perSession: new Map<string, { paths: string; result: GetResult | null }>(),
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

const F = {
  address: h("input", { placeholder: "host:port, e.g. 127.0.0.1:57400", value: store.get("address") }),
  tls: h("select", {},
    h("option", { value: "verify" }, "TLS, verify certificate"),
    h("option", { value: "skip" }, "TLS, skip verification"),
    h("option", { value: "plain" }, "Plaintext (insecure)")),
  ca: h("input", { placeholder: "CA file (optional)", value: store.get("ca") }),
  serverName: h("input", { placeholder: "server name override (optional)", value: store.get("serverName") }),
  username: h("input", { placeholder: "username", value: store.get("username"), autocomplete: "username" }),
  password: h("input", { type: "password", placeholder: "password (kept in memory only)", autocomplete: "current-password" }),
  jumps: h("input", { placeholder: "user@jump1:22, jump2 (optional)", value: store.get("jumps") }),
  identity: h("input", { placeholder: "key file (optional; agent and ~/.ssh keys are tried)", value: store.get("identity") }),
  timeout: h("input", { type: "number", min: "1", value: store.get("timeout") || "10" }),
};
F.tls.value = store.get("tls") || "skip";

const connectBtn = h("button", { class: "primary", onclick: () => connect() }, "Connect");
const disconnectBtn = h("button", { onclick: () => S.session && disconnect(S.session.id), disabled: true }, "Disconnect");
const connStatus = h("div", { class: "conn-status" }, "Not connected");

const presetSel = h("select", { title: "Saved connections" }, h("option", { value: "" }, "— new connection —"));
const presetSave = h("button", { onclick: () => savePreset() }, "Save as…");
const presetDel = h("button", { onclick: () => deletePreset(), disabled: true }, "Delete");
const presetInfo = h("div", { class: "muted small preset-info" });
const sessionsBar = h("div", { class: "sessions" });

const connPanel = h("section", { class: "conn-panel" },
  h("div", { class: "row" }, h("strong", {}, "Saved"), presetSel, presetSave, presetDel, presetInfo),
  h("div", { class: "conn-grid" },
    field("Target", F.address), field("Security", F.tls), field("CA file", F.ca), field("TLS server name", F.serverName),
    field("Username", F.username), field("Password", F.password), field("SSH jump hosts", F.jumps, "comma-separated chain"),
    field("SSH key file", F.identity), field("Timeout (s)", F.timeout)),
  h("div", { class: "conn-actions" }, connectBtn, disconnectBtn, connStatus),
  sessionsBar);

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
  h("div", { class: "row" }, yangInput, loadBtn), schemaInfo, searchInput, treeHost);
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

const connToggle = h("button", { class: "small", onclick: () => setConnCollapsed(!connPanel.classList.contains("collapsed")) }, "Hide connection form");
function setConnCollapsed(c: boolean) {
  connPanel.classList.toggle("collapsed", c);
  connToggle.textContent = c ? "Show connection form" : "Hide connection form";
}

function mount() {
  const app = $("#app");
  append(app,
    h("header", {}, h("div", { class: "brand" }, "Canopy"), h("div", { class: "muted small", id: "build" }),
      h("div", { class: "spacer" }), connToggle),
    connPanel,
    mainEl,
    footer);
  setupSplitters();
  showTab("Data");
  renderViewSel();
  let searchTimer = 0;
  searchInput.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = window.setTimeout(() => search(searchInput.value), 200);
  });
  pathsArea.addEventListener("input", () => tree.refresh());
  for (const el of [F.address, F.password, F.username]) {
    el.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Enter") connect(); });
  }
  yangInput.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Enter") loadSchema(); });
  api.info().then((i) => ($("#build").textContent = `${i.build.version} ${i.build.commit.slice(0, 8)}`)).catch(() => {});
  api.schema().then((s) => { if (s) schemaLoaded(s); }).catch(() => {});
  presetSel.addEventListener("change", () => applyPreset(presetSel.value));
  // Editing the address detaches the form from the preset.
  F.address.addEventListener("input", () => {
    if (S.preset) { S.preset = ""; presetSel.value = ""; showPresetInfo(); }
  });
  loadPresets();
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

// ---------- connection ----------

function target(): Target {
  const tls = F.tls.value;
  const jumps = F.jumps.value.split(",").map((s) => s.trim()).filter(Boolean);
  return {
    address: F.address.value.trim(),
    insecure: tls === "plain",
    skipVerify: tls === "skip",
    caFile: tls === "verify" ? F.ca.value.trim() || undefined : undefined,
    serverName: tls !== "plain" ? F.serverName.value.trim() || undefined : undefined,
    username: F.username.value.trim() || undefined,
    password: F.password.value || undefined,
    timeoutSec: Number(F.timeout.value) || 10,
    jumps: jumps.length ? jumps : undefined,
    identityFiles: F.identity.value.trim() ? [F.identity.value.trim()] : undefined,
    preset: S.preset || undefined,
  };
}

// ---------- saved connections ----------

async function loadPresets(select?: string) {
  try {
    S.presets = await api.presets();
  } catch (e) {
    presetInfo.textContent = errText(e);
    return;
  }
  clear(presetSel);
  presetSel.append(h("option", { value: "" }, "— new connection —"),
    ...S.presets.map((p) => h("option", { value: p.name }, p.name + (p.readOnly ? "  (lab/team)" : ""))));
  presetSel.value = select ?? S.preset;
  showPresetInfo();
}

function showPresetInfo() {
  const p = S.presets.find((x) => x.name === S.preset);
  presetDel.disabled = !p || !!p.readOnly;
  clear(presetInfo);
  if (!p) return;
  const parts = [p.description ?? ""];
  if (p.tags?.length) parts.push(p.tags.join(", "));
  if (p.lastSeen) parts.push(`last connected ${new Date(p.lastSeen.time).toLocaleString()} (${p.lastSeen.models} models, gNMI ${p.lastSeen.gnmiVersion})`);
  if (p.readOnly) parts.push(`read-only, from ${p.source}`);
  if (p.passwordEnv) parts.push(`password from $${p.passwordEnv}`);
  presetInfo.textContent = parts.filter(Boolean).join(" · ");
  presetInfo.title = p.source ?? "";
}

// applyPreset fills the form from a saved connection.
function applyPreset(name: string) {
  S.preset = name;
  const p = S.presets.find((x) => x.name === name);
  showPresetInfo();
  if (!p) return;
  F.address.value = p.address;
  F.tls.value = p.insecure ? "plain" : p.skipVerify ? "skip" : "verify";
  F.ca.value = p.caFile ?? "";
  F.serverName.value = p.serverName ?? "";
  F.username.value = p.username ?? "";
  F.password.value = "";
  F.jumps.value = (p.jumps ?? []).join(", ");
  F.identity.value = (p.identityFiles ?? [])[0] ?? "";
  F.timeout.value = String(p.timeoutSec || 10);
  if (p.yangDirs?.length) yangInput.value = p.yangDirs.join("; ");
}

async function savePreset() {
  const t = target();
  if (!t.address) { toast("Enter a target address first", "err"); return; }
  const existing = S.presets.find((x) => x.name === S.preset && !x.readOnly);
  const name = h("input", { value: existing?.name ?? "", placeholder: "name, e.g. lab-leaf1" });
  const desc = h("input", { value: existing?.description ?? "", placeholder: "description (optional)" });
  const tags = h("input", { value: (existing?.tags ?? []).join(", "), placeholder: "tags, comma-separated (optional)" });
  const pwEnv = h("input", { value: existing?.passwordEnv ?? "", placeholder: "GNMI_PASSWORD" });
  const withCtx = h("input", { type: "checkbox", checked: true });
  const body = h("div", { class: "form" },
    field("Name", name), field("Description", desc), field("Tags", tags),
    field("Password environment variable", pwEnv, "Passwords are never saved. Leave empty to use GNMI_PASSWORD, or type it in the form."),
    h("label", { class: "check" }, withCtx, " Also save the YANG folders, marked paths and encoding"));
  if ((await modal(existing ? `Update “${existing.name}”` : "Save connection", body, [{ label: "Save", primary: true, value: "save" }])) !== "save") return;
  const p: Preset = {
    name: name.value.trim(), description: desc.value.trim() || undefined,
    tags: tags.value.split(",").map((s) => s.trim()).filter(Boolean),
    address: t.address, insecure: t.insecure, skipVerify: t.skipVerify, caFile: t.caFile, serverName: t.serverName,
    username: t.username, passwordEnv: pwEnv.value.trim() || undefined, timeoutSec: t.timeoutSec,
    jumps: t.jumps, identityFiles: t.identityFiles,
  };
  if (withCtx.checked) {
    p.yangDirs = yangInput.value.split(";").map((s) => s.trim()).filter(Boolean);
    p.paths = markedPaths();
    p.encoding = encSel.value || undefined;
  }
  try {
    const saved = await api.savePreset(p);
    S.preset = saved.name;
    await loadPresets(saved.name);
    toast(`Saved “${saved.name}”`);
  } catch (e) {
    toast(errText(e), "err");
  }
}

async function deletePreset() {
  const p = S.presets.find((x) => x.name === S.preset);
  if (!p || p.readOnly) return;
  if ((await modal(`Delete “${p.name}”?`, h("p", {}, "The saved connection is removed. Open sessions stay connected."), [{ label: "Delete", primary: true, value: "del" }])) !== "del") return;
  try {
    await api.deletePreset(p.name);
    S.preset = "";
    await loadPresets("");
  } catch (e) {
    toast(errText(e), "err");
  }
}

// ---------- sessions ----------

function renderSessions() {
  clear(sessionsBar);
  if (!S.sessions.length) return;
  sessionsBar.append(h("span", { class: "muted small" }, "Open:"));
  for (const s of S.sessions) {
    const chip = h("span", { class: "chip" + (S.session?.id === s.id ? " active" : ""), title: `${s.address}${s.hops?.length ? " via " + s.hops.join(" → ") : ""}` },
      h("span", { class: "dot ok" }), s.label,
      h("button", { class: "x", title: "Disconnect", onclick: (e: Event) => { e.stopPropagation(); disconnect(s.id); } }, "×"));
    chip.addEventListener("click", () => activate(s.id));
    sessionsBar.append(chip);
  }
}

// activate makes a session the active one, restoring its paths, result and models.
async function activate(id: string) {
  const s = S.sessions.find((x) => x.id === id);
  if (!s) return;
  if (S.session && S.session.id !== id) {
    S.perSession.set(S.session.id, { paths: pathsArea.value, result: S.result });
  }
  S.session = s;
  const st = S.perSession.get(id);
  pathsArea.value = st?.paths ?? (s.paths ?? []).join("\n");
  S.result = st?.result ?? null;
  if (S.result) buildData(S.result.data);
  resultMeta.textContent = "";
  renderData();
  connected();
  if (s.yangDirs?.length) {
    const want = s.yangDirs.join("; ");
    if (yangInput.value !== want || !S.schema) {
      yangInput.value = want;
      await loadSchema();
    }
  }
  refreshTree();
}

async function connect() {
  const t = target();
  if (!t.address) { toast("Enter a target address", "err"); return; }
  for (const k of ["address", "ca", "serverName", "username", "jumps", "identity", "timeout"] as const) {
    store.set(k, (F as any)[k].value);
  }
  store.set("tls", F.tls.value);
  connectBtn.disabled = true;
  connStatus.className = "conn-status busy";
  connStatus.textContent = `Connecting to ${t.address}…`;
  try {
    for (let attempt = 0; attempt < 4; attempt++) {
      try {
        const s = await api.connect(t);
        S.sessions.push(s);
        F.password.value = "";
        await activate(s.id);
        setConnCollapsed(true); // more room for the tree and data; the header button shows it again
        if (s.preset) loadPresets(); // refresh "last connected"
        return;
      } catch (e) {
        if (!(e instanceof ApiError)) throw e;
        const d = e.err.details ?? {};
        if (e.err.code === "host_key_unknown") {
          const ok = await modal("Unknown SSH host key",
            h("div", {}, h("p", {}, `The host key of ${d.jumpHost} is not in known_hosts.`),
              h("pre", { class: "fp" }, `${d.keyType}\n${d.fingerprint}`),
              h("p", { class: "muted small" }, "Compare it with the fingerprint from the host's administrator. Trusting adds it to known_hosts.")),
            [{ label: "Trust and connect", primary: true, value: "yes" }]);
          if (ok !== "yes") throw e;
          t.acceptHostKeys = [...(t.acceptHostKeys ?? []), d.fingerprint];
          t.saveHostKeys = true;
          continue;
        }
        if (e.err.code === "passphrase_required") {
          const inp = h("input", { type: "password", placeholder: "passphrase" });
          const v = await new Promise<string | null>((res) => {
            inp.addEventListener("keydown", (ev: KeyboardEvent) => { if (ev.key === "Enter") { (inp.closest(".modal-back") as HTMLElement).remove(); res(inp.value); } });
            modal(`Passphrase for ${d.keyFile}`, h("div", {}, inp), [{ label: "Unlock", primary: true, value: "ok" }])
              .then((r) => res(r === "ok" ? inp.value : null));
          });
          if (v === null) throw e;
          t.passphrases = { ...(t.passphrases ?? {}), [d.keyFile]: v };
          continue;
        }
        throw e;
      }
    }
  } catch (e) {
    connStatus.className = "conn-status err";
    connStatus.textContent = errText(e);
  } finally {
    connectBtn.disabled = false;
  }
}

function connected() {
  const s = S.session!;
  connStatus.className = "conn-status ok";
  connStatus.textContent = `Active: ${s.label}${s.label !== s.address ? ` (${s.address})` : ""}${s.hops?.length ? " via " + s.hops.join(" → ") : ""} · gNMI ${s.capabilities.gnmiVersion} · ${s.capabilities.models.length} models · ${s.connectMs} ms`;
  disconnectBtn.disabled = false;
  clear(encSel);
  encSel.append(h("option", { value: "" }, `encoding: auto (${s.encoding})`),
    ...s.capabilities.encodings.map((e) => h("option", { value: e }, e)));
  renderCaps();
  renderSessions();
  renderFooter();
}

async function disconnect(id: string) {
  await api.disconnect(id).catch(() => {});
  S.sessions = S.sessions.filter((x) => x.id !== id);
  S.perSession.delete(id);
  if (S.session?.id === id) {
    S.session = null;
    const next = S.sessions[S.sessions.length - 1];
    if (next) { await activate(next.id); return; }
    S.result = null;
    renderData();
    connStatus.className = "conn-status";
    connStatus.textContent = "Not connected";
    disconnectBtn.disabled = true;
    renderCaps();
  }
  renderSessions();
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
  append(row, caret, cb, kindBadge(n), h("span", { class: "name" }, n.name),
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
  for (const a of d.ancestors) {
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
  append(details,
    h("h2", {}, kindBadge(n), " ", n.name),
    h("div", { class: "path-box" },
      h("code", {}, n.path),
      h("button", { class: "small", onclick: () => copyText(n.path, "path") }, "Copy"),
      h("button", { class: "small", onclick: () => copyText(n.qpath, "module-qualified path") }, "Copy qualified")),
    h("div", { class: "row" },
      h("button", { class: "primary", onclick: () => { if (!markedPaths().includes(n.path)) toggleMark(n.path, true); showTab("Data"); doGet([n.path]); } }, "Get this path"),
      h("button", { onclick: () => { toggleMark(n.path, !marked); showDetails(d); } }, marked ? "Unmark" : "Mark")),
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
    buildData(r.data);
    resultMeta.textContent = `${r.type} · ${r.encoding} · ${r.updateCount} updates · ${r.leafCount} leaves · ${(r.sizeBytes / 1024).toFixed(1)} KiB · ${r.elapsedMs} ms`;
    renderData();
  } catch (e) {
    S.result = null;
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
  else { const t = target(); delete t.password; req.target = t; }
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
