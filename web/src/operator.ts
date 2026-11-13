// Resources tab (network-operator workspace): the resources that configure the
// cluster Device selected in the sidebar, with pause/resume and QA verification.
// Listing needs only the cluster; Verify connects to the Device when needed.

import { K8sResource, opApi, OpOptions, Sample, Session, Verification } from "./api";
import { append, clear, copyText, field, h, modal, toast, type Child } from "./dom";
import { errText } from "./config";
import { help } from "./help";
import { stringify } from "./json";

export interface ClusterDevice {
  namespace: string; name: string; provider: string; ready: string; paused: boolean; connected: boolean;
  address?: string; route?: string; // route: how the open connection goes (directly / via jump hosts)
  hostname?: string; manufacturer?: string; model?: string; firmware?: string; // what the operator found on the device
}

// reported names what the operator found on the device (the Device's status).
function reported(d: { hostname?: string; manufacturer?: string; model?: string; firmware?: string }) {
  return [d.hostname, [d.manufacturer, d.model].filter(Boolean).join(" "), d.firmware].filter(Boolean).join(" · ");
}

export interface OpContext {
  device(): ClusterDevice | null; // selected in the cluster list
  session(): Promise<Session | null>; // its connection, connecting if needed
  browse(): Promise<void>; // open its connection in the Devices workspace
  disconnect(): Promise<void>;
  options(): OpOptions;
  toFixture?(r: K8sResource): void;
  deviceChanged?(): void; // the Device itself changed (paused): reload the cluster list
}

// shortProvider names a provider by its vendor folder: srlinux.nokia.networking.metal.ironcore.dev → nokia/srlinux
export function shortProvider(p: string) {
  const [a, b] = p.split(".");
  return b && p.includes(".networking.") ? `${b}/${a}` : p;
}

function badge(status: string, kind: "ready" | "verdict" | "leaf" = "ready") {
  const cls = { True: "ok", pass: "ok", ok: "ok", other: "ok", False: "err", fail: "err", mismatch: "err", missing: "err", present: "err", error: "err" }[status] ?? "muted";
  const text = kind === "ready" ? (status === "True" ? "Ready" : status === "False" ? "Not ready" : "Unknown") : status;
  return h("span", { class: `badge ${cls}` }, text);
}

export function operatorTab(ctx: OpContext) {
  const host = h("div", { class: "operator op-split" });
  const head = h("div", { class: "row op-head-body" });
  const list = h("div", { class: "op-list" });
  // results (Verify, Spec, errors) go to the side panel, next to the lists,
  // so pressing a button doesn't scroll the page away from it
  const result = h("div", { class: "op-result" });
  const side = h("aside", { class: "op-side" },
    h("div", { class: "op-side-head" }, h("span", { class: "muted small section-label" }, "Result"), h("span", { class: "grow" }),
      h("button", { class: "small icon", title: "Clear", onclick: () => clear(result) }, "✕")),
    result,
    h("p", { class: "muted small op-side-empty" }, "Verify, Spec and errors show here. Verify a resource or a sample to compare its intent with the device."));
  const kindList = h("datalist", { id: "op-kinds" });
  const delKind = h("input", { placeholder: "kind, e.g. Banner", list: "op-kinds" });
  const delName = h("input", { placeholder: "name (optional)" });
  const delBox = h("details", { class: "op-card op-deleted" });

  const dev = () => ctx.device();
  const showResult = (...nodes: Child[]) => {
    clear(result);
    append(result, ...nodes);
    side.scrollTop = 0;
  };
  const knownKinds = new Set<string>();
  const addKinds = (ks: string[]) => {
    const n = knownKinds.size;
    ks.forEach((k) => knownKinds.add(k));
    if (knownKinds.size === n) return;
    clear(kindList);
    [...knownKinds].sort().forEach((k) => kindList.append(h("option", { value: k })));
  };

  const verify = async (kind: string, name: string, deleted = false, yaml = "") => {
    const d = dev();
    if (!d) { showResult(h("p", { class: "err" }, "Select a Device in the cluster list first.")); return; }
    showResult(h("p", { class: "muted small" }, d.connected ? `Verifying ${kind} ${name}…` : `Connecting to ${d.namespace}/${d.name}, then verifying ${kind} ${name}…`));
    try {
      const s = await ctx.session();
      if (!s) { showResult(h("p", { class: "err" }, "Not connected to the Device: see the Console.")); return; }
      refreshHead();
      showVerification(await opApi.verify(ctx.options(), s.id, kind, d.namespace, name, deleted, yaml));
    } catch (e) { showResult(h("p", { class: "err" }, errText(e))); }
  };

  const showVerification = (v: Verification) => {
    const v2s = (x: unknown) => (x === undefined || x === null ? "" : typeof x === "string" ? JSON.stringify(x) : stringify(x, 0));
    const route = v.jumps?.length ? `via ${v.jumps.join(", ")}` : "directly";
    const fact = (k: string, ...val: Child[]) => [h("dt", {}, k), h("dd", {}, ...val)];
    const info = v.deviceInfo ? reported(v.deviceInfo) : "";
    showResult(
      h("div", { class: "row between" },
        h("h4", {}, `${v.resource.kind || ""} ${v.resource.name} · ${v.mode}`, " ", badge(v.verdict, "verdict")),
        h("button", { class: "small", onclick: () => copyText(stringify(v), "verification JSON") }, "Copy report")),
      v.reasons.length ? h("ul", { class: "reasons" }, ...v.reasons.map((r) => h("li", {}, r))) : null,
      h("dl", { class: "op-facts small" },
        ...fact("Device", v.device.replace(/^k8s:/, ""), info ? h("span", { class: "muted" }, ` (reports ${info})`) : null),
        ...fact("Read from", h("span", { class: "mono" }, v.address ?? "?"), ` ${route}`),
        ...fact("Provider", h("span", { class: "mono", title: "The Device's spec.provider: it picks the fixtures folder" }, v.provider ?? "?")),
        ...fact("Fixture", h("span", { class: "mono" }, v.fixtureFile || v.fixture), " ",
          v.fixtureFile ? h("button", { class: "link small", onclick: () => copyText(v.fixtureFile!, "fixture path") }, "copy") : null),
        ...(v.alternates?.length ? fact("Other fixtures", v.alternates.join(", ")) : []),
        ...fact("Took", `${v.elapsedMs} ms`)),
      h("h4", { class: "small" }, `${v.leaves.length} ${v.leaves.length === 1 ? "leaf" : "leaves"} checked`),
      ...v.leaves.map((l) => {
        const changed = !!l.fixturePath && l.fixturePath !== l.path;
        const fxVal = l.fixtureValue !== undefined && v2s(l.fixtureValue) !== v2s(l.expected) ? v2s(l.fixtureValue) : "";
        return h("div", { class: "vleaf" },
          h("div", { class: "vleaf-head" }, badge(l.status, "leaf"),
            h("span", { class: "mono", title: "The gNMI path read from the device" }, l.path)),
          h("dl", { class: "op-facts small" },
            ...fact("In the fixture", l.fixturePath
              ? h("span", { class: changed ? "mono" : "muted" }, changed ? l.fixturePath : "the same path")
              : h("span", { class: "muted" }, "?")),
            ...fact(v.mode === "deleted" ? "The resource set" : "Expected", h("span", { class: "mono" }, v2s(l.expected)),
              l.from ? h("span", { class: "muted" }, ` (spec.${l.from})`) : null),
            ...(fxVal ? fact("Fixture value", h("span", { class: "mono muted" }, fxVal)) : []),
            ...fact(v.mode === "deleted" ? "On the device now" : "On the device",
              h("span", { class: "mono" + (l.status === "error" ? " err" : "") }, v2s(l.actual) || (l.status === "ok" && v.mode === "deleted" ? "(nothing)" : "")))));
      }));
  };

  const reasonOf = (r: K8sResource) => {
    const c = r.conditions?.find((x) => x.type === "Ready");
    const t = [c?.reason, c?.message].filter(Boolean).join(": ");
    return t ? h("span", { class: "muted small cond", title: t }, t) : null;
  };

  const pause = async (kind: string, name: string, paused: boolean) => {
    const d = dev();
    if (!d) return;
    try {
      const r = await opApi.pause(ctx.options(), kind, d.namespace, name, paused);
      toast(`${kind} ${name}: ${paused ? "paused" : "resumed"}` + (r.condition ? ` (Paused=${r.condition})` : ""));
      if (kind === "device") ctx.deviceChanged?.();
      await loadResources(true);
    } catch (e) { toast(errText(e), "err"); }
  };

  const notReconciled = (r: K8sResource) => !!r.observedGeneration && r.observedGeneration !== r.generation;
  const row = (r: K8sResource) => h("tr", {},
    h("td", {}, r.kind), h("td", {}, r.name),
    h("td", { class: "status" }, badge(r.ready),
      r.paused ? h("span", { class: "badge warn" }, "paused") : null,
      notReconciled(r) ? h("span", { class: "badge muted", title: `The operator hasn't reconciled the latest change yet (generation ${r.generation}, observed ${r.observedGeneration})` }, "reconciling") : null,
      r.ready !== "True" ? reasonOf(r) : null),
    h("td", { class: "actions" }, h("span", { class: "slots res" },
      h("button", { class: "small primary", title: "Check that this resource's intent is on the device", onclick: () => verify(r.kind, r.name) }, "Verify"),
      h("button", { class: "small", onclick: () => pause(r.kind, r.name, !r.paused) }, r.paused ? "Resume" : "Pause"),
      ctx.toFixture ? h("button", { class: "small", title: "Build a test fixture for this resource (Fixtures tab)", onclick: () => ctx.toFixture!(r) }, "Fixture") : null,
      h("button", { class: "small icon", title: "Show spec", onclick: () => showResult(h("h4", {}, `${r.kind} ${r.name} · spec`), h("pre", { class: "code" }, stringify(r.spec ?? {}))) }, "Spec"))));

  const refreshHead = () => {
    clear(head);
    const d = dev();
    if (!d) return;
    append(head,
      h("div", { class: "op-dev" },
        h("div", { class: "op-dev-name" }, h("strong", {}, `${d.namespace}/${d.name}`), badge(d.ready),
          d.paused ? h("span", { class: "badge warn" }, "paused") : null),
        h("div", { class: "muted small" }, h("span", { title: d.provider }, shortProvider(d.provider)), " · ",
          d.connected ? h("span", { class: "ok" }, "connected") : "not connected (Verify connects)"),
        h("dl", { class: "op-facts small" },
          h("dt", {}, "Provider"), h("dd", { class: "mono", title: "The Device's spec.provider, set when the Device was created. The operator configures the device through it, and Verify takes its fixtures from this provider's folder." }, d.provider),
          h("dt", {}, "Address"), h("dd", { class: "mono" }, d.address || "?", d.route ? h("span", { class: "muted" }, ` (${d.route})`) : null),
          h("dt", {}, "Device reports"), h("dd", { title: "What the operator found on the device (the Device's status)" }, reported(d) || h("span", { class: "muted" }, "nothing yet")))),
      h("span", { class: "grow" }),
      h("div", { class: "row" },
        h("button", { class: "small", title: d.paused ? "Resume the Device: the operator reconciles it again" : "Pause the Device: the operator stops reconciling it and all its resources", onclick: () => pause("device", d.name, !d.paused) }, d.paused ? "Resume device" : "Pause device"),
        h("button", { class: "small", title: "Open this Device's connection in the Devices workspace: schema, data, snapshots (connects if needed)", onclick: () => ctx.browse() }, "Browse in Devices"),
        d.connected ? h("button", { class: "small", onclick: async () => { await ctx.disconnect(); refreshHead(); } }, "Disconnect") : null));
  };

  // ----- samples: network-operator's example resources, applied like Tilt's buttons.
  // Loaded when the section is opened (once per Device), and kept: an apply or
  // delete updates its row instead of loading everything again.
  const samplesInfo = h("p", { class: "muted small" });
  const samplesList = h("div", { class: "op-list" });
  const sampleFilter = h("input", { type: "search", placeholder: "Filter samples (kind or name)" });
  let samples: Sample[] = [];
  let samplesKey = ""; // the Device the list is for
  let samplesSeq = 0;
  const samplesCount = h("span", { class: "muted small" });
  const samplesBox = h("details", { class: "op-card op-samples" },
    h("summary", {}, h("h3", {}, "Samples", help("samples")), samplesCount,
      h("span", { class: "muted small" }, "network-operator's example resources, applied like Tilt's sample buttons")),
    h("div", { class: "row" }, sampleFilter, h("button", { class: "small", title: "Read the samples and their state again", onclick: () => loadSamples(true) }, "Reload")),
    samplesInfo, samplesList);
  samplesBox.addEventListener("toggle", () => { if (samplesBox.open) loadSamples(); });
  const devKey = () => { const d = dev(); return d ? `${d.namespace}/${d.name}/${d.provider}` : ""; };

  const countSamples = () => {
    if (!samplesKey) { samplesCount.textContent = ""; return; }
    const applied = samples.filter((x) => x.applied && !x.otherDevice).length;
    const missing = samples.filter((x) => x.notServed).length;
    samplesCount.textContent = `${samples.length} · ${applied} applied` + (missing ? ` · ${missing} not served` : "");
  };
  const drawSamples = () => {
    countSamples();
    clear(samplesList);
    const q = sampleFilter.value.trim().toLowerCase();
    const shown = samples.filter((x) => !q || x.kind.toLowerCase().includes(q) || x.name.toLowerCase().includes(q))
      .sort((a, b) => Number(!!a.notServed) - Number(!!b.notServed));
    if (!shown.length) { samplesList.append(h("p", { class: "muted small" }, samples.length ? "No sample matches." : "")); return; }
    // fixed action slots, so the buttons line up from row to row
    const slot = (b: HTMLElement | null) => h("span", { class: "slot" }, b);
    samplesList.append(h("table", { class: "grid op-table" },
      h("thead", {}, h("tr", {}, h("th", {}, "Kind"), h("th", {}, "Name"), h("th", {}, "In the cluster"), h("th", {}, ""))),
      h("tbody", {}, ...shown.map((x) => h("tr", { class: x.notServed ? "muted" : "" },
        h("td", {}, x.kind),
        h("td", {}, x.name, x.vendor || x.secrets?.length
          ? h("div", { class: "muted small" }, [x.vendor, x.secrets?.length ? `with Secret ${x.secrets.join(", ")}` : ""].filter(Boolean).join(" · ")) : null),
        h("td", { class: "status" }, x.notServed ? h("span", { class: "muted small", title: "The cluster doesn't serve this kind (another provider's, or a CRD that isn't installed)" }, "kind not served")
          : x.otherDevice ? h("span", { class: "badge warn", title: "An object with this name exists and points at another Device" }, `used by ${x.otherDevice}`)
          : x.applied ? h("span", { class: "badge ok" }, "applied") : h("span", { class: "muted small" }, "not applied")),
        h("td", { class: "actions" }, x.notServed ? null : h("span", { class: "slots" },
          slot(h("button", { class: "small primary", title: "Review, edit and apply it to this Device", onclick: () => applySample(x) }, x.applied ? "Re-apply…" : "Apply…")),
          slot(x.otherDevice ? null : x.applied
            ? h("button", { class: "small", title: "Check on the device that this sample's configuration is there (connects to it)", onclick: () => verify(x.kind, x.name) }, "Verify")
            : h("button", { class: "small", title: "Check on the device that this sample's configuration is not there, e.g. after deleting it (connects to it)", onclick: () => verify(x.kind, x.name, true, x.yaml) }, "Verify removal")),
          slot(x.applied && !x.otherDevice ? h("button", { class: "small", title: "Delete it from the cluster; the operator removes its configuration from the device", onclick: () => deleteSample(x) }, "Delete") : null))))))));
  };
  sampleFilter.addEventListener("input", drawSamples);

  const loadSamples = async (force = false) => {
    const d = dev();
    const key = devKey();
    if (!d) { samples = []; samplesKey = ""; samplesInfo.textContent = ""; drawSamples(); return; }
    if (force || key !== samplesKey) samplesCount.textContent = "";
    if (!force && key === samplesKey) return; // already loaded for this Device
    const seq = ++samplesSeq;
    samplesInfo.textContent = "Loading samples…";
    try {
      const r = await opApi.samples(ctx.options(), d.namespace, d.name, d.provider);
      if (seq !== samplesSeq) return; // another Device was selected meanwhile
      samples = r.samples;
      addKinds(samples.filter((x) => !x.notServed).map((x) => x.kind));
      samplesKey = key;
      samplesInfo.textContent = `From ${r.dir}: the common samples and ${shortProvider(d.provider)}'s. Apply aims them at ${d.namespace}/${d.name}.`;
    } catch (e) {
      if (seq !== samplesSeq) return;
      samplesInfo.textContent = errText(e);
    }
    drawSamples();
  };

  const applySample = async (x: Sample) => {
    const d = dev();
    if (!d) return;
    const text = h("textarea", { rows: 16, class: "mono", spellcheck: false });
    text.value = x.yaml;
    const r = await modal(`Apply ${x.kind} ${x.name} to ${d.namespace}/${d.name}`, h("div", { class: "form" },
      h("p", { class: "muted small" }, `Canopy sets the namespace to ${d.namespace}, spec.deviceRef and the device-name label to ${d.name}, and applies it (server-side apply, like kubectl apply --server-side). The operator then changes the device's configuration: lab devices only.`),
      text), [{ label: "Apply", primary: true, value: "apply" }]);
    if (r !== "apply") return;
    try {
      const res = await opApi.apply(ctx.options(), d.namespace, d.name, text.value);
      toast(`Applied ${res.applied.join(", ")}`);
      x.applied = true; x.otherDevice = undefined;
      drawSamples();
      loadResources(true);
    } catch (e) { toast(errText(e), "err"); }
  };
  const deleteSample = async (x: Sample) => {
    const d = dev();
    if (!d) return;
    const what = [`${x.kind} ${x.name}`, ...(x.secrets ?? []).map((s) => `Secret ${s}`)].join(", ");
    const r = await modal(`Delete ${x.kind} ${x.name}?`, h("p", {}, `Deletes ${what} from ${d.namespace}. The operator removes the configuration from ${d.name}.`),
      [{ label: "Delete", primary: true, value: "yes" }]);
    if (r !== "yes") return;
    try {
      const res = await opApi.remove(ctx.options(), d.namespace, x.yaml);
      toast(`Deleted ${res.deleted.join(", ")}`);
      x.applied = false;
      drawSamples();
      loadResources(true);
    } catch (e) { toast(errText(e), "err"); }
  };

  // ----- the Device's resources. quiet keeps the table while it reloads.
  const resNote = h("span", { class: "muted small" });
  const resCount = h("span", { class: "muted small" });
  let resSeq = 0;
  let resKey = "";
  const loadResources = async (quiet = false) => {
    const d = dev();
    if (!d) return;
    const key = devKey();
    const seq = ++resSeq;
    if (!quiet || key !== resKey) { clear(list); resCount.textContent = ""; list.append(h("p", { class: "muted small" }, "Loading resources…")); }
    resNote.textContent = quiet ? "updating…" : "";
    try {
      const rs = await opApi.resources(ctx.options(), d.namespace, d.name);
      if (seq !== resSeq) return; // a newer load (or another Device) won
      resKey = key;
      resNote.textContent = "";
      clear(list);
      addKinds(rs.map((x) => x.kind));
      const bad = rs.filter((x) => x.ready !== "True").length, paused = rs.filter((x) => x.paused).length;
      resCount.textContent = rs.length ? `${rs.length} · ${bad ? `${bad} not ready` : "all ready"}` + (paused ? ` · ${paused} paused` : "") : "";
      if (!rs.length) { list.append(h("p", { class: "muted small" }, "No resources reference this Device (spec.deviceRef).")); return; }
      list.append(h("table", { class: "grid op-table" },
        h("thead", {}, h("tr", {}, h("th", {}, "Kind"), h("th", {}, "Name"), h("th", {}, "Status"), h("th", {}, ""))),
        h("tbody", {}, ...rs.map(row))));
    } catch (e) {
      if (seq !== resSeq) return;
      resNote.textContent = "";
      clear(list); list.append(h("p", { class: "err" }, errText(e)));
    }
  };

  const refresh = async () => {
    const d = dev();
    clear(head);
    resBox.hidden = samplesBox.hidden = delBox.hidden = !d;
    if (!d) {
      clear(list); clear(result);
      head.append(h("p", { class: "muted" }, "Select a Device in the cluster list (left) to see the network-operator resources that configure it."));
      loadSamples();
      return;
    }
    refreshHead();
    if (samplesBox.open) loadSamples();
    await loadResources(devKey() === resKey);
  };

  append(delBox, h("summary", {}, h("h3", {}, "Verify another deleted resource")),
      h("p", { class: "muted small" }, "For a resource that isn't a sample (samples have Verify removal on their row): after you delete it, checks on the device that its configuration is gone. " +
        "The resource no longer exists, so the paths come from this provider's test fixture for the kind; the name only labels the report."),
      h("div", { class: "row" }, field("Kind", delKind), field("Name", delName),
        h("button", { onclick: () => {
          const k = delKind.value.trim();
          if (!k) { showResult(h("p", { class: "err" }, "Enter the kind of the deleted resource (e.g. Banner).")); delKind.focus(); return; }
          verify(k, delName.value.trim() || k.toLowerCase(), true);
        } }, "Verify removal")), kindList);
  const resBox = h("section", { class: "op-card" },
    h("div", { class: "op-card-head" }, h("h3", {}, "Resources", help("operator")), resCount, resNote, h("span", { class: "grow" }),
      h("button", { class: "small", title: "Read the resources again", onclick: () => loadResources(true) }, "Refresh")),
    h("p", { class: "muted small" }, "The resources whose spec.deviceRef is this Device. Verify checks on the device that a resource's configuration is there."),
    list);
  append(host, h("div", { class: "op-split-main" }, h("div", { class: "op-card op-head" }, head), resBox, samplesBox, delBox), side);
  return { el: host, refresh };
}
