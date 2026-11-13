// Downloaded models: what is in the model cache, how big it is, removing sets
// that aren't needed, and storing identical files once.

import { CacheInfo, cacheApi, ModelSource } from "./api";
import { append, clear, h, modal, toast } from "./dom";
import { errText } from "./config";
import { help } from "./help";

export function humanBytes(b: number): string {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return Math.round(b / (1 << 10)) + " KB";
  return b + " B";
}

export interface CacheContext {
  sources(): ModelSource[];
  loaded(): { source: string; version: string } | null; // the set in use, if any
  changed(): void; // after a removal (refresh version lists)
}

export async function openCacheDialog(ctx: CacheContext) {
  const host = h("div", { class: "cache-list" });
  const summary = h("p", { class: "small" });
  const shareNote = h("p", { class: "muted small" });
  const shareBtn = h("button", { title: "Store identical files once (hard links)" }, "Share identical files");
  const name = (s: { kind: string; source: string }) =>
    s.kind === "device" ? "From a device (NETCONF)" : s.kind === "shared" ? "Imports (shared)" : ctx.sources().find((x) => x.id === s.source)?.name ?? s.source;

  const render = (info: CacheInfo) => {
    clear(host);
    summary.textContent = `${info.sets.length} downloaded set${info.sets.length === 1 ? "" : "s"}, ${humanBytes(info.bytes)}, in ${info.dir}`;
    const sh = info.share;
    shareNote.textContent = sh && sh.linked
      ? `${sh.linked} files are stored once and shared between sets (last run ${new Date(sh.time).toLocaleDateString()}). Sizes below count shared files in every set.`
      : "Many releases contain the same files. Sharing stores each identical file once (hard links); Canopy never edits cached files, so nothing else changes.";
    if (!info.sets.length) { host.append(h("p", { class: "muted small pad" }, "Nothing downloaded yet.")); return; }
    const inUse = ctx.loaded();
    host.append(h("table", { class: "grid" },
      h("thead", {}, h("tr", {}, h("th", {}, "Models"), h("th", {}, "Version"), h("th", {}, "Files"), h("th", {}, "Size"), h("th", {}, "Downloaded"), h("th", {}, ""))),
      h("tbody", {}, ...info.sets.map((s) => {
        const used = inUse && inUse.source === s.source && inUse.version === s.version;
        return h("tr", {},
          h("td", {}, name(s), used ? h("span", { class: "badge ok small", title: "Loaded now" }, " in use") : null),
          h("td", { class: "mono", title: s.dir }, s.version),
          h("td", {}, String(s.files)),
          h("td", {}, humanBytes(s.bytes)),
          h("td", {}, new Date(s.downloaded).toLocaleDateString()),
          h("td", { class: "actions" }, h("button", { class: "small", title: "Delete from the cache; it is downloaded again when needed", onclick: async () => {
            try {
              await cacheApi.remove(s);
              toast(`Removed ${name(s)} ${s.version}`);
              ctx.changed();
              render(await cacheApi.list());
            } catch (e) { toast(errText(e), "err"); }
          } }, "Remove")));
      }))));
  };

  shareBtn.addEventListener("click", async () => {
    shareBtn.disabled = true;
    try {
      const r = await cacheApi.share();
      toast(`${r.linked} of ${r.files} files shared; saved ${humanBytes(r.savedBytes)}`);
      render(await cacheApi.list());
    } catch (e) { toast(errText(e), "err"); } finally { shareBtn.disabled = false; }
  });

  const body = h("div", { class: "form" },
    h("div", { class: "row" }, summary, help("cache")),
    host,
    h("div", { class: "row" }, shareBtn), shareNote);
  try { render(await cacheApi.list()); } catch (e) { append(host, h("p", { class: "err" }, errText(e))); }
  modal("Downloaded models", body, [], { wide: true });
}
