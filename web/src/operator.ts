// Operator tab: the network-operator resources attached to the active Device,
// with QA verification and pause/resume.

import { K8sResource, opApi, OpOptions, Session, Verification } from "./api";
import { append, clear, copyText, field, h, toast } from "./dom";
import { errText } from "./config";
import { stringify } from "./json";

export interface OpContext {
  session(): Session | null;
  options(): OpOptions;
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

  const dev = () => ctx.session()?.device;

  const verify = async (kind: string, name: string, deleted = false) => {
    const s = ctx.session(), d = dev();
    if (!s || !d) return;
    clear(result);
    result.append(h("p", { class: "muted small" }, `Verifying ${kind} ${name}…`));
    try {
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
      h("button", { class: "small icon", title: "Show spec", onclick: () => { clear(result); result.append(h("h4", {}, `${r.kind} ${r.name} spec`), h("pre", { class: "code" }, stringify(r.spec ?? {}))); } }, "{}")));

  const refresh = async () => {
    clear(head); clear(list);
    const s = ctx.session(), d = dev();
    if (!s || !d) {
      head.append(h("p", { class: "muted" }, "Connect a device from the Cluster list (left) to see the network-operator resources that configure it."));
      return;
    }
    append(head,
      h("div", { class: "row between" },
        h("div", {}, h("strong", {}, `${d.namespace}/${d.name}`), h("span", { class: "muted small" }, ` · ${d.provider}`)),
        h("div", { class: "row" },
          h("button", { class: "small", title: "Pause the Device (pauses all its resources)", onclick: () => pause("device", d.name, true) }, "Pause device"),
          h("button", { class: "small", onclick: () => pause("device", d.name, false) }, "Resume device"),
          h("button", { class: "small", onclick: refresh }, "Refresh"))));
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
    h("p", { class: "muted small" }, "Verify takes the provider's test fixture for the resource's kind (network-operator test/gnmi/testdata), maps the device leaves it writes onto this resource's spec, and reads them from the device."),
    head, list,
    h("details", { class: "op-deleted" }, h("summary", { class: "muted small" }, "Verify a deleted resource"),
      h("div", { class: "row" }, field("Kind", delKind), field("Name", delName),
        h("button", { onclick: () => { if (delKind.value && delName.value) verify(delKind.value.trim(), delName.value.trim(), true); } }, "Verify removal"))),
    result);
  return { el: host, refresh };
}
