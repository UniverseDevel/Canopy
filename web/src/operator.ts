// Resources tab (network-operator workspace): the resources that configure the
// cluster Device selected in the sidebar, with pause/resume and QA verification.
// Listing needs only the cluster; Verify connects to the Device when needed.

import { K8sResource, opApi, OpOptions, Sample, Session, Verification } from "./api";
import { append, clear, copyText, field, h, modal, toast } from "./dom";
import { errText } from "./config";
import { help } from "./help";
import { stringify } from "./json";

export interface ClusterDevice { namespace: string; name: string; provider: string; ready: string; paused: boolean; connected: boolean }

export interface OpContext {
  device(): ClusterDevice | null; // selected in the cluster list
  session(): Promise<Session | null>; // its connection, connecting if needed
  browse(): Promise<void>; // open its connection in the Devices workspace
  disconnect(): Promise<void>;
  options(): OpOptions;
  toFixture?(r: K8sResource): void;
}

function badge(status: string, kind: "ready" | "verdict" | "leaf" = "ready") {
  const cls = { True: "ok", pass: "ok", ok: "ok", False: "err", fail: "err", mismatch: "err", missing: "err", present: "err", error: "err" }[status] ?? "muted";
  const text = kind === "ready" ? (status === "True" ? "Ready" : status === "False" ? "Not ready" : "Unknown") : status;
  return h("span", { class: `badge ${cls}` }, text);
}

export function operatorTab(ctx: OpContext) {
  const host = h("div", { class: "operator" });
  const head = h("div", { class: "op-head" });
  const list = h("div", { class: "op-list" });
  const result = h("div", { class: "op-result" });
  const delKind = h("input", { placeholder: "kind, e.g. banner" });
  const delName = h("input", { placeholder: "name" });

  const dev = () => ctx.device();

  const verify = async (kind: string, name: string, deleted = false) => {
    const d = dev();
    if (!d) return;
    clear(result);
    result.append(h("p", { class: "muted small" }, d.connected ? `Verifying ${kind} ${name}…` : `Connecting to ${d.namespace}/${d.name}, then verifying ${kind} ${name}…`));
    try {
      const s = await ctx.session();
      if (!s) { clear(result); result.append(h("p", { class: "err" }, "Not connected to the Device: see the Console.")); return; }
      refreshHead();
      showVerification(await opApi.verify(ctx.options(), s.id, kind, d.namespace, name, deleted));
    } catch (e) { clear(result); result.append(h("p", { class: "err" }, errText(e))); }
  };

  const showVerification = (v: Verification) => {
    clear(result);
    const v2s = (x: unknown) => (x === undefined || x === null ? "" : typeof x === "string" ? JSON.stringify(x) : stringify(x, 0));
    append(result,
      h("div", { class: "row between" },
        h("h4", {}, `${v.resource.kind || ""} ${v.resource.name} · ${v.mode}`, " ", badge(v.verdict, "verdict")),
        h("button", { class: "small", onclick: () => copyText(stringify(v), "verification JSON") }, "Copy report")),
      h("p", { class: "muted small" }, `Device ${v.device} · fixture ${v.fixture} · ${v.elapsedMs} ms` + (v.alternates?.length ? ` · other fixtures: ${v.alternates.join(", ")}` : "")),
      v.reasons.length ? h("ul", { class: "reasons" }, ...v.reasons.map((r) => h("li", {}, r))) : null,
      h("table", { class: "grid verify-table" },
        h("thead", {}, h("tr", {}, h("th", {}, ""), h("th", {}, "Device path"), h("th", {}, v.mode === "deleted" ? "" : "Expected"), h("th", {}, v.mode === "deleted" ? "" : "On the device"), h("th", {}, "From spec"))),
        h("tbody", {}, ...v.leaves.map((l) => h("tr", {},
          h("td", {}, badge(l.status, "leaf")), h("td", { class: "mono" }, l.path),
          h("td", { class: "mono" }, v.mode === "deleted" ? "" : v2s(l.expected)), h("td", { class: "mono" }, v.mode === "deleted" ? "" : v2s(l.actual)),
          h("td", { class: "mono muted" }, l.from ?? ""))))));
  };

  const pause = async (kind: string, name: string, paused: boolean) => {
    const d = dev();
    if (!d) return;
    try {
      const r = await opApi.pause(ctx.options(), kind, d.namespace, name, paused);
      toast(`${kind} ${name}: Paused=${r.condition || paused}`);
      await refresh();
    } catch (e) { toast(errText(e), "err"); }
  };

  const row = (r: K8sResource) => h("tr", {},
    h("td", {}, r.kind), h("td", {}, r.name), h("td", {}, badge(r.ready)),
    h("td", {}, r.paused ? h("span", { class: "badge warn" }, "paused") : ""),
    h("td", { class: "muted small" }, r.observedGeneration && r.observedGeneration !== r.generation ? `gen ${r.generation}/${r.observedGeneration}` : ""),
    h("td", { class: "actions" },
      h("button", { class: "small primary", title: "Check that this resource's intent is on the device", onclick: () => verify(r.kind, r.name) }, "Verify"),
      h("button", { class: "small", onclick: () => pause(r.kind, r.name, !r.paused) }, r.paused ? "Resume" : "Pause"),
      ctx.toFixture ? h("button", { class: "small", title: "Build a test fixture for this resource (Fixtures tab)", onclick: () => ctx.toFixture!(r) }, "Fixture") : null,
      h("button", { class: "small icon", title: "Show spec", onclick: () => { clear(result); result.append(h("h4", {}, `${r.kind} ${r.name} spec`), h("pre", { class: "code" }, stringify(r.spec ?? {}))); } }, "{}")));

  const refreshHead = () => {
    clear(head);
    const d = dev();
    if (!d) return;
    append(head,
      h("div", { class: "row between" },
        h("div", {}, h("strong", {}, `${d.namespace}/${d.name}`), h("span", { class: "muted small" }, ` · ${d.provider} · ${d.connected ? "connected" : "not connected"}`)),
        h("div", { class: "row" },
          h("button", { class: "small", title: "Pause the Device (pauses all its resources)", onclick: () => pause("device", d.name, true) }, "Pause device"),
          h("button", { class: "small", onclick: () => pause("device", d.name, false) }, "Resume device"),
          h("button", { class: "small", onclick: refresh }, "Refresh"),
          h("button", { class: "small", title: "Open this Device's connection in the Devices workspace: schema, data, snapshots (connects if needed)", onclick: () => ctx.browse() }, "Browse in Devices"),
          d.connected ? h("button", { class: "small", onclick: async () => { await ctx.disconnect(); refreshHead(); } }, "Disconnect") : null)));
  };

  // ----- samples: network-operator's example resources, applied like Tilt's buttons
  const samplesInfo = h("p", { class: "muted small" });
  const samplesList = h("div", { class: "op-list" });
  const sampleFilter = h("input", { type: "search", placeholder: "Filter samples (kind or name)" });
  let samples: Sample[] = [];
  const drawSamples = () => {
    clear(samplesList);
    const q = sampleFilter.value.trim().toLowerCase();
    const shown = samples.filter((x) => !q || x.kind.toLowerCase().includes(q) || x.name.toLowerCase().includes(q));
    if (!shown.length) { samplesList.append(h("p", { class: "muted small" }, samples.length ? "No sample matches." : "")); return; }
    samplesList.append(h("table", { class: "grid op-table" },
      h("thead", {}, h("tr", {}, h("th", {}, "Kind"), h("th", {}, "Name"), h("th", {}, ""), h("th", {}, "In the cluster"), h("th", {}, ""))),
      h("tbody", {}, ...shown.map((x) => h("tr", {},
        h("td", {}, x.kind), h("td", {}, x.name),
        h("td", { class: "muted small" }, [x.vendor, x.secrets?.length ? `+ Secret ${x.secrets.join(", ")}` : ""].filter(Boolean).join(" · ")),
        h("td", {}, x.otherDevice ? h("span", { class: "badge warn", title: "An object with this name exists and points at another Device" }, `used by ${x.otherDevice}`)
          : x.applied ? h("span", { class: "badge ok" }, "applied") : h("span", { class: "muted small" }, "—")),
        h("td", { class: "actions" },
          h("button", { class: "small primary", title: "Review, edit and apply it to this Device", onclick: () => applySample(x) }, x.applied ? "Re-apply…" : "Apply…"),
          x.applied && !x.otherDevice ? h("button", { class: "small", title: "Delete it from the cluster; the operator removes its configuration from the device", onclick: () => deleteSample(x) }, "Delete") : null))))));
  };
  sampleFilter.addEventListener("input", drawSamples);
  const loadSamples = async () => {
    const d = dev();
    clear(samplesList);
    samples = [];
    if (!d) { samplesInfo.textContent = ""; return; }
    samplesInfo.textContent = "Loading samples…";
    try {
      const r = await opApi.samples(ctx.options(), d.namespace, d.name, d.provider);
      samples = r.samples;
      samplesInfo.textContent = `${samples.length} samples from ${r.dir} (the common ones and this provider's folder). Apply aims them at ${d.namespace}/${d.name}.`;
    } catch (e) { samplesInfo.textContent = errText(e); }
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
    } catch (e) { toast(errText(e), "err"); }
    await refresh();
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
    } catch (e) { toast(errText(e), "err"); }
    await refresh();
  };

  const refresh = async () => {
    clear(head); clear(list);
    const d = dev();
    loadSamples();
    if (!d) {
      head.append(h("p", { class: "muted" }, "Select a Device in the cluster list (left) to see the network-operator resources that configure it."));
      return;
    }
    refreshHead();
    list.append(h("p", { class: "muted small" }, "Loading resources…"));
    try {
      const rs = await opApi.resources(ctx.options(), d.namespace, d.name);
      clear(list);
      if (!rs.length) { list.append(h("p", { class: "muted small" }, "No resources reference this Device (spec.deviceRef).")); return; }
      list.append(h("table", { class: "grid op-table" },
        h("thead", {}, h("tr", {}, h("th", {}, "Kind"), h("th", {}, "Name"), h("th", {}, "Status"), h("th", {}, ""), h("th", {}, ""), h("th", {}, ""))),
        h("tbody", {}, ...rs.map(row))));
    } catch (e) { clear(list); list.append(h("p", { class: "err" }, errText(e))); }
  };

  append(host,
    h("p", { class: "muted small" }, "The network-operator resources of the selected Device. Pause, verify on the device (connects to it), or start a test fixture. ", help("operator")),
    head, list,
    h("h3", {}, "Samples", help("samples")),
    h("p", { class: "muted small" }, "network-operator's example resources (config/samples of the checkout chosen in Fixtures), like Tilt's sample buttons, aimed at this Device."),
    h("div", { class: "row" }, sampleFilter), samplesInfo, samplesList,
    h("details", { class: "op-deleted" }, h("summary", { class: "muted small" }, "Verify a deleted resource"),
      h("div", { class: "row" }, field("Kind", delKind), field("Name", delName),
        h("button", { onclick: () => { if (delKind.value && delName.value) verify(delKind.value.trim(), delName.value.trim(), true); } }, "Verify removal"))),
    result);
  return { el: host, refresh };
}
