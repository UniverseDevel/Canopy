// Configuration dialog: build a change for a schema node, validate and preview
// it, and apply it only after an explicit confirmation.

import { ApiError, configApi, DiffResult, SetOutcome, SNode, Session } from "./api";
import { append, clear, copyText, field, h, modal, toast } from "./dom";
import { parse, stringify } from "./json";
import { withHelp } from "./help";

export function errText(e: unknown): string {
  if (e instanceof ApiError) {
    const x = e.err;
    let s = x.message;
    if (x.grpcCode) s += ` (gRPC ${x.grpcCode})`;
    if (x.hint) s += ` — ${x.hint}`;
    return `[${x.code}] ${s}`;
  }
  return String(e);
}

// fillKeys turns "/a[k=*]/b" into "/a[k=]/b" so the user sees where keys go.
function keyHint(path: string): string {
  return path.replace(/=\*\]/g, "=]");
}

export async function openConfigDialog(n: SNode, session: Session, onApplied: () => void, opt: { path?: string; shell?: () => string } = {}) {
  const path = h("input", { value: keyHint(opt.path ?? n.path), class: "mono" });
  const op = h("select", {},
    h("option", { value: "update" }, "update (merge)"),
    h("option", { value: "replace" }, "replace (remove what isn't given)"),
    h("option", { value: "delete" }, "delete"));
  const value = h("textarea", { rows: 12, class: "mono", spellcheck: false });
  const leafHelper = h("div", { class: "row" });
  if (n.kind === "leaf" && n.type?.enums?.length) {
    const sel = h("select", {}, ...n.type.enums.map((e) => h("option", { value: e }, e)));
    sel.addEventListener("change", () => (value.value = JSON.stringify(sel.value)));
    leafHelper.append(h("span", { class: "muted small" }, "Value:"), sel);
    value.value = JSON.stringify(n.type.enums[0]);
  } else if (n.kind === "leaf" && n.type?.base === "boolean") {
    const sel = h("select", {}, h("option", { value: "true" }, "true"), h("option", { value: "false" }, "false"));
    sel.addEventListener("change", () => (value.value = sel.value));
    leafHelper.append(h("span", { class: "muted small" }, "Value:"), sel);
    value.value = "true";
  }
  const out = h("div", { class: "cfg-out" });
  const fromDevice = h("button", { class: "small", onclick: async () => {
    if (path.value.includes("=]")) { toast("Fill in the list keys in the path first", "err"); return; }
    try {
      const r = await configApi.current(session.id, path.value.trim());
      const u = r.notifications.flatMap((x) => x.updates ?? []);
      value.value = stringify(u.length === 1 ? u[0].value : r.data);
      if (r.empty) toast("The device has no config here yet");
    } catch (e) { toast(errText(e), "err"); }
  } }, "Start from the device's config");
  const fromSchema = h("button", { class: "small", onclick: async () => {
    try { value.value = stringify((await configApi.skeleton(n.id)).value); } catch (e) { toast(errText(e), "err"); }
  } }, "Start from the schema");
  const syncOp = () => { value.disabled = op.value === "delete"; };
  op.addEventListener("change", syncOp);
  if (!value.value && n.kind !== "leaf") fromSchema.click();

  const confirm = h("input", { type: "checkbox" });
  const confirmLabel = h("label", { class: "check confirm" }, confirm,
    ` I want to change the configuration of ${session.label} (${session.address}). Only lab devices, with the owner's approval.`);

  const body = h("div", { class: "form" },
    h("div", { class: "form-grid two" }, field("Path (fill in list keys)", path), field(withHelp("Operation", "configure"), op)),
    h("div", { class: "row" }, fromDevice, fromSchema, leafHelper),
    field("Value (JSON, as JSON_IETF)", value),
    out, confirmLabel);

  const ops = () => {
    const p = path.value.trim();
    if (op.value === "delete") return [{ op: "delete", path: p }];
    let v: unknown;
    try { v = parse(value.value); } catch (e) { throw new Error("The value is not valid JSON: " + (e as Error).message); }
    return [{ op: op.value, path: p, value: v }];
  };

  const show = (o: SetOutcome | undefined, err?: string) => {
    clear(out);
    if (err) out.append(h("p", { class: "err" }, err));
    if (!o) return;
    if (o.problems.length) {
      out.append(h("div", { class: "problems" }, h("strong", {}, "Validation"),
        h("ul", {}, ...o.problems.map((p) => h("li", {}, p)))));
    } else {
      out.append(h("p", { class: "ok small" }, "✓ Valid against the loaded models"));
    }
    if (o.destructive) out.append(h("p", { class: "warn small" }, "⚠ Replace/delete removes existing configuration."));
    append(out,
      h("details", { open: !o.sent }, h("summary", {}, "SetRequest preview"), h("pre", { class: "code" }, o.preview)),
      ...o.gnmic.map((c) => h("div", { class: "cmd-preview inline" },
        h("div", { class: "cmd-bar" }, h("span", { class: "cmd-label" }, c.title), h("span", { class: "grow" }),
          h("button", { class: "small", onclick: () => copyText(c.text, "command") }, "Copy")),
        h("code", { class: "cmd-line" }, c.text),
        c.note ? h("div", { class: "muted small cmd-note" }, c.note) : null)));
    if (o.sent) {
      out.append(h("p", { class: "ok" }, `✓ Applied in ${o.result?.elapsedMs ?? "?"} ms`));
      if (o.diff) out.append(renderDiff(o.diff));
    }
  };

  // Live preview: validate and show the equivalent command while editing
  // (a dry run; nothing is sent).
  let timer = 0;
  const live = () => {
    clearTimeout(timer);
    timer = window.setTimeout(async () => {
      let list;
      try { list = ops(); } catch { return; }
      if (list.some((x) => x.path.includes("=]") || x.path.includes("=*]"))) return;
      try { show(await configApi.set({ session: session.id, ops: list, dryRun: true, shell: opt.shell?.() })); } catch { /* shown on Validate */ }
    }, 500);
  };
  for (const el of [path, op, value]) { el.addEventListener("input", live); el.addEventListener("change", live); }
  live();

  await modal(`Configure ${n.name}`, body, [
    { label: "Validate & preview", value: "dry" },
    { label: "Apply", value: "apply", primary: true, danger: true },
  ], {
    wide: true,
    keepOpen: async (v) => {
      let list;
      try { list = ops(); } catch (e) { show(undefined, (e as Error).message); return true; }
      if (list.some((x) => x.path.includes("=]") || x.path.includes("=*]"))) { show(undefined, "Fill in the list keys in the path."); return true; }
      if (v === "apply" && !confirm.checked) { show(undefined, "Tick the confirmation first."); confirmLabel.classList.add("attention"); return true; }
      try {
        const o = await configApi.set({ session: session.id, ops: list, dryRun: v === "dry", confirm: v === "apply", shell: opt.shell?.() });
        show(o);
        if (o.sent) { confirm.checked = false; onApplied(); }
      } catch (e) {
        const o = e instanceof ApiError ? (e.body?.outcome as SetOutcome | undefined) : undefined;
        show(o, errText(e));
      }
      return true;
    },
  });
}

export function renderDiff(d: DiffResult): HTMLElement {
  const box = h("div", { class: "diff" },
    h("p", { class: "small" }, `${d.added} added · ${d.removed} removed · ${d.changed} changed · ${d.unchanged} unchanged`));
  if (!d.changes.length) { box.append(h("p", { class: "muted small" }, "No differences.")); return box; }
  const v = (x: unknown) => (typeof x === "string" ? JSON.stringify(x) : stringify(x, 0));
  box.append(h("table", { class: "grid diff-table" }, h("tbody", {}, ...d.changes.map((c) =>
    h("tr", { class: "d-" + c.kind },
      h("td", { class: "mono" }, { added: "+", removed: "−", changed: "~" }[c.kind] ?? "?"),
      h("td", { class: "mono" }, c.path),
      h("td", { class: "mono" }, c.kind === "added" ? "" : v(c.before)),
      h("td", { class: "mono" }, c.kind === "removed" ? "" : v(c.after)))))));
  return box;
}
