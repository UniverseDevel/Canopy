// Snapshots tab: save reads of the marked paths, list them, compare two
// snapshots or a snapshot with the device now.

import { Session, SnapMeta, snapApi } from "./api";
import { append, clear, field, h, toast } from "./dom";
import { errText, renderDiff } from "./config";

export interface SnapContext {
  session(): Session | null;
  paths(): string[];
  type(): string;
}

const DEFAULT_IGNORE = "statistics, counters, current-datetime, up-time, last-change, last-booted";

export function snapshotsTab(ctx: SnapContext) {
  const host = h("div", { class: "snapshots" });
  const name = h("input", { placeholder: "name (optional)" });
  const note = h("input", { placeholder: "note (optional)" });
  const onlyDevice = h("input", { type: "checkbox", checked: true });
  const ignore = h("input", { value: DEFAULT_IGNORE, title: "Paths containing these are left out of comparisons" });
  const list = h("div", { class: "snap-list" });
  const result = h("div", { class: "snap-result" });
  const selected = new Set<string>();
  let items: SnapMeta[] = [];

  const ign = () => ignore.value.split(",").map((s) => s.trim()).filter(Boolean);

  const take = async () => {
    const s = ctx.session();
    if (!s) { toast("Connect a device first", "err"); return; }
    const paths = ctx.paths();
    if (!paths.length) { toast("Mark paths (Data tab) to snapshot", "err"); return; }
    try {
      const m = await snapApi.take(s.id, paths, ctx.type(), name.value.trim() || undefined, note.value.trim() || undefined);
      name.value = ""; note.value = "";
      toast(`Saved ${m.name} (${m.leaves} leaves)`);
      await refresh();
    } catch (e) { toast(errText(e), "err"); }
  };

  const compare = async (a: string, b: string) => {
    const s = ctx.session();
    if (!b && !s) { toast("Connect the device to compare with it", "err"); return; }
    clear(result);
    result.append(h("p", { class: "muted small" }, "Comparing…"));
    try {
      const d = await snapApi.diff(a, b, s?.id ?? "", ign());
      clear(result);
      append(result, h("h4", {}, b ? `${a} → ${b}` : `${a} → device now`), renderDiff(d));
    } catch (e) { clear(result); result.append(h("p", { class: "err" }, errText(e))); }
  };

  const draw = () => {
    clear(list);
    const s = ctx.session();
    const shown = items.filter((m) => !onlyDevice.checked || !s || m.device === s.label);
    if (!shown.length) { list.append(h("p", { class: "muted small" }, "No snapshots yet.")); return; }
    for (const m of shown) {
      const cb = h("input", { type: "checkbox", checked: selected.has(m.name), title: "Select two to compare" });
      cb.addEventListener("change", () => { cb.checked ? selected.add(m.name) : selected.delete(m.name); });
      list.append(h("div", { class: "snap" }, cb,
        h("div", { class: "snap-main" },
          h("div", { class: "snap-name" }, m.name),
          h("div", { class: "muted small" }, `${m.device} · ${new Date(m.time).toLocaleString()} · ${m.leaves} leaves · ${m.type}${m.note ? " · " + m.note : ""}`),
          h("div", { class: "muted small mono snap-paths", title: m.paths.join("\n") }, m.paths.join("  "))),
        h("button", { class: "small", title: "Compare with the device now (same paths)", onclick: () => compare(m.name, "") }, "vs now"),
        h("button", { class: "small icon", title: "Delete", onclick: async () => {
          try { await snapApi.remove(m.name); selected.delete(m.name); await refresh(); } catch (e) { toast(errText(e), "err"); }
        } }, "🗑")));
    }
  };

  const refresh = async () => {
    try { items = await snapApi.list(); } catch (e) { toast(errText(e), "err"); }
    draw();
  };
  onlyDevice.addEventListener("change", draw);

  append(host,
    h("p", { class: "muted small" }, "A snapshot saves a read of the marked paths (Data tab) with the device and time, to compare later: before/after a change, or the device now."),
    h("div", { class: "row" }, name, note, h("button", { class: "primary", onclick: take }, "Take snapshot")),
    h("div", { class: "row" },
      h("button", { onclick: () => {
        const [a, b] = [...selected];
        if (selected.size !== 2) { toast("Select exactly two snapshots", "err"); return; }
        compare(a, b);
      } }, "Compare selected"),
      h("label", { class: "check small" }, onlyDevice, "This device only"),
      h("button", { class: "small", onclick: refresh }, "Refresh")),
    field("Ignore in comparisons", ignore),
    list, result);
  return { el: host, refresh };
}
