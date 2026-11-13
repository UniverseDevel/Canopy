// Model catalog dialog: search the indexed releases for modules, see which
// releases contain a module, index more releases, and load one.

import { CatalogEntry, catalogApi, ModelSource, modelsApi } from "./api";
import { append, clear, h, modal, toast } from "./dom";
import { errText } from "./config";
import { help } from "./help";

export interface CatalogContext {
  sources(): ModelSource[];
  load(source: string, version: string): Promise<boolean>;
}

export async function openCatalog(ctx: CatalogContext) {
  const srcName = (id: string) => ctx.sources().find((s) => s.id === id)?.name ?? id;
  const q = h("input", { type: "search", placeholder: "Module, prefix, namespace or words from the description", class: "grow" });
  const src = h("select", {}, h("option", { value: "" }, "all sources"), ...ctx.sources().map((s) => h("option", { value: s.id }, s.name)));
  const releasesLine = h("div", { class: "muted small" });
  const results = h("div", { class: "cat-results" });
  const side = h("div", { class: "cat-side" }, h("p", { class: "muted small" }, "Click a module to see every indexed release that has it."));

  const idxSrc = h("select", {}, ...ctx.sources().map((s) => h("option", { value: s.id }, s.name)));
  const idxVer = h("select", {}, h("option", { value: "" }, "version"));
  const idxBtn = h("button", { class: "small" }, "Index");

  const loadReleases = async () => {
    try {
      const rs = await catalogApi.releases();
      const full = rs.filter((r) => r.enriched).length;
      releasesLine.textContent = rs.length
        ? `${rs.length} release${rs.length === 1 ? "" : "s"} indexed (${full} downloaded, ${rs.length - full} from file names) · ${rs.reduce((n, r) => n + r.files, 0)} modules`
        : "The catalog is empty. Downloaded model sets are added automatically; index other releases below.";
    } catch (e) { releasesLine.textContent = errText(e); }
  };

  const loadBtn = (source: string, version: string) =>
    h("button", { class: "small", title: `Load ${srcName(source)} ${version} as the schema`, onclick: async () => {
      if (await ctx.load(source, version)) { toast(`Loaded ${srcName(source)} ${version}`); (results.closest(".modal-back") as HTMLElement | null)?.remove(); }
    } }, "Load");

  const showContaining = async (module: string) => {
    clear(side);
    side.append(h("h4", {}, module), h("p", { class: "muted small" }, "Loading…"));
    try {
      const es = await catalogApi.search({ module, limit: 1000 });
      clear(side);
      side.append(h("h4", {}, module), h("p", { class: "muted small" }, `In ${es.length} indexed release${es.length === 1 ? "" : "s"}`));
      side.append(h("table", { class: "grid cat-table" },
        h("thead", {}, h("tr", {}, h("th", {}, "Release"), h("th", {}, "Revision"), h("th", {}, ""))),
        h("tbody", {}, ...es.map((e) => h("tr", {},
          h("td", {}, `${srcName(e.source)} ${e.version}`),
          h("td", { class: "mono" }, (e.revision ?? "") + (e.ocVersion ? ` (${e.ocVersion})` : "")),
          h("td", {}, loadBtn(e.source, e.version)))))));
      const one = es.find((e) => e.enriched);
      if (one) {
        append(side,
          one.namespace ? h("p", { class: "small" }, h("span", { class: "muted" }, "namespace "), h("code", {}, one.namespace)) : null,
          one.imports?.length ? h("p", { class: "small" }, h("span", { class: "muted" }, "imports "), one.imports.join(", ")) : null,
          one.deviates?.length ? h("p", { class: "small" }, h("span", { class: "muted" }, "deviates "), one.deviates.join(", ")) : null,
          one.description ? h("p", { class: "small" }, one.description) : null);
      }
    } catch (e) { clear(side); side.append(h("p", { class: "err" }, errText(e))); }
  };

  const search = async () => {
    clear(results);
    if (!q.value.trim() && !src.value) { results.append(h("p", { class: "muted small pad" }, "Type to search.")); return; }
    try {
      const es: CatalogEntry[] = await catalogApi.search({ q: q.value, source: src.value || undefined, limit: 200 });
      if (!es.length) { results.append(h("p", { class: "muted small pad" }, "No matches in the indexed releases.")); return; }
      results.append(h("table", { class: "grid cat-table" },
        h("thead", {}, h("tr", {}, h("th", {}, "Module"), h("th", {}, "Revision"), h("th", {}, "Release"), h("th", {}, "Prefix"))),
        h("tbody", {}, ...es.map((e) => h("tr", { class: "clickable", title: e.description ?? "", onclick: () => showContaining(e.module) },
          h("td", {}, h("span", { class: "mono" }, e.module), e.kind === "submodule" ? h("span", { class: "muted small" }, " submodule") : null,
            e.deviates?.length ? h("span", { class: "badge small" }, "deviations") : null),
          h("td", { class: "mono" }, (e.revision ?? "") + (e.ocVersion ? ` (${e.ocVersion})` : "")),
          h("td", {}, `${srcName(e.source)} ${e.version}`),
          h("td", { class: "mono" }, e.prefix ?? ""))))));
    } catch (e) { results.append(h("p", { class: "err" }, errText(e))); }
  };

  const fillVersions = async () => {
    clear(idxVer);
    idxVer.append(h("option", { value: "" }, "loading…"));
    try {
      const vs = await modelsApi.versions(idxSrc.value);
      clear(idxVer);
      idxVer.append(...vs.map((v) => h("option", { value: v.name }, v.name + (v.cached ? "  ✓" : ""))));
    } catch (e) { clear(idxVer); idxVer.append(h("option", { value: "" }, "listing failed")); toast(errText(e), "err"); }
  };
  idxSrc.addEventListener("change", fillVersions);
  idxBtn.addEventListener("click", async () => {
    if (!idxVer.value) return;
    idxBtn.disabled = true;
    try {
      const r = await catalogApi.index(idxSrc.value, idxVer.value);
      toast(`Indexed ${srcName(r.source)} ${r.version}: ${r.files} modules${r.enriched ? "" : " (file names)"}`);
      await loadReleases();
      search();
    } catch (e) { toast(errText(e), "err"); } finally { idxBtn.disabled = false; }
  });

  let timer = 0;
  q.addEventListener("input", () => { clearTimeout(timer); timer = window.setTimeout(search, 200); });
  src.addEventListener("change", search);

  const body = h("div", { class: "catalog" },
    h("div", { class: "row" }, q, src, help("catalog")),
    releasesLine,
    h("div", { class: "cat-cols" }, results, side),
    h("details", { class: "cat-index" }, h("summary", { class: "muted small" }, "Index another release (lists its files on GitHub; nothing is downloaded)"),
      h("div", { class: "row" }, idxSrc, idxVer, idxBtn)));
  loadReleases();
  search();
  fillVersions();
  modal("Model catalog", body, [], { wide: true });
  q.focus();
}
