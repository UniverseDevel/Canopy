// Fixtures tab: build network-operator test fixtures (.txtar) from snapshots,
// and run fixtures against the operator in the cluster with a shadow target.

import { api, FixtureRun, fixturesApi, K8sResource, OpOptions, Session, snapApi } from "./api";
import { append, clear, copyText, field, h, toast } from "./dom";
import { errText } from "./config";
import { help, withHelp } from "./help";
import { stringify } from "./json";

export interface FixtureContext {
  session(): Session | null;
  options(): OpOptions;
}

const store = {
  get(k: string, d = ""): string { try { return localStorage.getItem("canopy.fx." + k) ?? d; } catch { return d; } },
  set(k: string, v: string) { try { localStorage.setItem("canopy.fx." + k, v); } catch { /* unavailable */ } },
};

export function fixturesTab(ctx: FixtureContext) {
  const host = h("div", { class: "fixtures" });

  // ----- run settings -----
  const provider = h("select", {});
  const fixture = h("select", {});
  const mode = h("select", {},
    h("option", { value: "embedded" }, "Canopy runs a shadow target"),
    h("option", { value: "remote" }, "My shadow target (canopy shadow --http)"));
  mode.value = store.get("mode", "embedded");
  const listen = h("input", { value: store.get("listen", ":57500"), placeholder: ":57500" });
  const tls = h("input", { type: "checkbox", checked: store.get("tls", "1") === "1" });
  const control = h("input", { value: store.get("control"), placeholder: "http://127.0.0.1:57480" });
  const devAddr = h("input", { value: store.get("devAddr"), placeholder: "e.g. 172.18.0.1:57500 or host.docker.internal:57500" });
  const keep = h("input", { type: "checkbox" });
  const timeout = h("input", { type: "number", min: "10", value: store.get("timeout", "60") });
  const embeddedFields = h("div", { class: "form-grid" },
    field("Listen on", listen, "where Canopy serves the shadow target"),
    h("label", { class: "check" }, tls, " TLS (the operator connects with TLS)"));
  const remoteFields = h("div", { class: "form-grid" }, field("Control URL", control, "the shadow target's --http address"));
  const syncMode = () => { embeddedFields.hidden = mode.value !== "embedded"; remoteFields.hidden = mode.value !== "remote"; };
  mode.addEventListener("change", syncMode);
  syncMode();
  const runOut = h("div", { class: "fx-run-out" });
  const viewOut = h("div", {});

  const runOpts = () => {
    store.set("mode", mode.value); store.set("listen", listen.value.trim()); store.set("tls", tls.checked ? "1" : "0");
    store.set("control", control.value.trim()); store.set("devAddr", devAddr.value.trim()); store.set("timeout", timeout.value);
    store.set("provider", provider.value);
    return {
      provider: provider.value, deviceAddress: devAddr.value.trim(),
      shadowControl: mode.value === "remote" ? control.value.trim() : undefined,
      shadowListen: mode.value === "embedded" ? listen.value.trim() : undefined,
      tls: tls.checked, keep: keep.checked, timeoutSec: Number(timeout.value) || 60,
    };
  };

  const loadProviders = async () => {
    try {
      const r = await fixturesApi.list(ctx.options());
      clear(provider);
      provider.append(...r.providers.map((p) => h("option", { value: p }, p)));
      const want = ctx.session()?.device?.provider || store.get("provider");
      if (want && r.providers.includes(want)) provider.value = want;
      repoLine.textContent = `network-operator checkout: ${r.repo}`;
      repoLine.className = "muted small";
      await loadFixtures();
    } catch (e) {
      repoLine.textContent = errText(e);
      repoLine.className = "err small";
    }
  };
  const loadFixtures = async () => {
    clear(fixture);
    if (!provider.value) return;
    try {
      const r = await fixturesApi.list(ctx.options(), provider.value);
      fixture.append(...r.fixtures.map((f) => h("option", { value: f.file, title: f.title }, `${f.file} — ${f.kinds.join(", ")}`)));
    } catch (e) { toast(errText(e), "err"); }
  };
  provider.addEventListener("change", loadFixtures);
  const repoLine = h("p", { class: "muted small" }, "");

  const run = async (text?: string, name?: string) => {
    const o = runOpts();
    if (!o.provider) { toast("Pick a provider", "err"); return; }
    if (!o.deviceAddress) { toast("Enter the address the operator uses to reach the shadow target", "err"); return; }
    const file = text ? (name || "built.txtar") : fixture.value;
    if (!file) { toast("Pick a fixture", "err"); return; }
    clear(runOut);
    runOut.append(h("p", { class: "muted small" }, `Running ${file}: creating a namespace, a Device and the resources; waiting for the operator… (up to a few minutes)`));
    try {
      showRun(await fixturesApi.run(ctx.options(), { ...o, file, text }));
    } catch (e) {
      clear(runOut);
      runOut.append(h("p", { class: "err" }, errText(e)));
    }
  };

  const showRun = (r: FixtureRun) => {
    clear(runOut);
    const v = (x: unknown) => (x === undefined ? "" : typeof x === "string" ? JSON.stringify(x) : stringify(x, 0));
    append(runOut,
      h("div", { class: "row between" },
        h("h4", {}, `${r.fixture} `, h("span", { class: `badge ${r.verdict === "pass" ? "ok" : "err"}` }, r.verdict.toUpperCase())),
        h("button", { class: "small", onclick: () => copyText(stringify(r), "run report JSON") }, "Copy report")),
      h("p", { class: "muted small" }, `${r.title} · namespace ${r.namespace || "—"} · ${(r.elapsedMs / 1000).toFixed(1)} s`),
      h("table", { class: "grid fx-steps" }, h("tbody", {}, ...r.steps.map((s) => h("tr", {},
        h("td", {}, h("span", { class: `badge ${s.ok ? "ok" : "err"}` }, s.ok ? "ok" : "fail")),
        h("td", {}, s.step), h("td", { class: "small" }, s.detail ?? ""))))),
      r.diff?.length ? h("div", {}, h("h4", {}, "Differences (expected → on the shadow target)"),
        h("table", { class: "grid diff-table" }, h("tbody", {}, ...r.diff.map((c) => h("tr", { class: "d-" + c.kind },
          h("td", { class: "mono" }, { added: "+", removed: "−", changed: "~" }[c.kind] ?? "?"),
          h("td", { class: "mono" }, c.path), h("td", { class: "mono" }, v(c.before)), h("td", { class: "mono" }, v(c.after))))))) : null);
  };

  // ----- build -----
  const title = h("input", { placeholder: "e.g. Banner PreLogin" });
  const cr = h("textarea", { rows: 9, class: "mono", spellcheck: false, placeholder: "apiVersion: networking.metal.ironcore.dev/v1alpha1\nkind: Banner\nmetadata:\n  name: banner\nspec:\n  deviceRef:\n    name: device\n  ..." });
  const snapSel = () => h("select", {}, h("option", { value: "" }, "—"));
  const pre = snapSel(), exp = snapSel(), del = snapSel();
  const out = h("textarea", { rows: 14, class: "mono", spellcheck: false, placeholder: "The fixture appears here; you can edit it before saving or running." });
  const saveName = h("input", { placeholder: "file name, e.g. banner-prelogin" });

  const loadSnaps = async () => {
    try {
      const l = await snapApi.list();
      for (const sel of [pre, exp, del]) {
        const cur = sel.value;
        clear(sel);
        sel.append(h("option", { value: "" }, "—"), ...l.map((m) => h("option", { value: m.name }, `${m.name} (${m.device}, ${m.leaves} leaves)`)));
        sel.value = cur;
      }
    } catch { /* none */ }
  };

  const build = async () => {
    if (!cr.value.trim() || !exp.value) { toast("The resource YAML and an expect snapshot are needed", "err"); return; }
    try {
      out.value = (await fixturesApi.export({ title: title.value.trim(), cr: cr.value, preload: pre.value, expect: exp.value, delete: del.value })).text;
    } catch (e) { toast(errText(e), "err"); }
  };
  const save = async () => {
    if (!out.value.trim()) { toast("Build the fixture first", "err"); return; }
    if (!saveName.value.trim()) { toast("Enter a file name", "err"); return; }
    try {
      const r = await fixturesApi.save(ctx.options(), provider.value, saveName.value.trim(), out.value);
      toast(`Saved ${r.file}`);
      loadFixtures();
    } catch (e) { toast(errText(e), "err"); }
  };

  append(host,
    h("section", { class: "fx-section" },
      h("p", { class: "muted small" }, "Test cases for network-operator: a resource and the device state it must produce. ", help("fixtures")),
      h("h3", {}, "Run a fixture", help("fixtureRun")),
      h("p", { class: "muted small" }, "Runs the fixture against the operator in the cluster, with a shadow target in place of a device."),
      repoLine,
      h("div", { class: "form-grid" }, field("Provider", provider), field("Fixture", fixture), field(withHelp("Shadow target", "shadow"), mode),
        field("Address the operator uses", devAddr, "how pods in the cluster reach the shadow target")),
      embeddedFields, remoteFields,
      h("div", { class: "row" }, field("Wait (s)", timeout), h("label", { class: "check" }, keep, " Keep the namespace"),
        h("span", { class: "grow" }),
        h("button", { onclick: async () => {
          if (!fixture.value) return;
          try {
            const t = (await fixturesApi.read(ctx.options(), provider.value, fixture.value)).text;
            clear(viewOut);
            viewOut.append(h("details", { open: true }, h("summary", {}, fixture.value), h("pre", { class: "code" }, t)));
          } catch (e) { toast(errText(e), "err"); }
        } }, "View"),
        h("button", { class: "primary", onclick: () => run() }, "Run")),
      viewOut, runOut),
    h("section", { class: "fx-section" },
      h("h3", {}, "Build a fixture from snapshots", help("fixtureBuild")),
      h("p", { class: "muted small" }, "Snapshots before, after applying the resource, and after deleting it, plus the resource's YAML."),
      h("div", { class: "form-grid" }, field("Title", title), field("state/preload", pre), field("state/expect", exp), field("state/delete", del)),
      field("Resource YAML", cr),
      h("div", { class: "row" }, h("button", { onclick: loadSnaps }, "Reload snapshots"), h("button", { class: "primary", onclick: build }, "Build")),
      field("Fixture", out),
      h("div", { class: "row" },
        h("button", { onclick: () => copyText(out.value, "fixture") }, "Copy"),
        saveName,
        h("button", { title: "Write into test/gnmi/testdata/<provider>/ of the network-operator checkout", onclick: save }, "Save to provider folder"),
        h("button", { onclick: () => { if (out.value.trim()) run(out.value, (saveName.value.trim() || "built") + ".txtar"); } }, "Run this fixture"))));

  // useResource fills the build form from a cluster resource.
  const useResource = async (r: K8sResource) => {
    const obj = {
      apiVersion: `networking.metal.ironcore.dev/${r.version || "v1alpha1"}`, kind: r.kind,
      metadata: { name: r.name, namespace: "default" }, spec: { ...(r.spec ?? {}), deviceRef: { name: "device" } },
    };
    try { cr.value = (await api.format("yaml", obj)).text; } catch { cr.value = stringify(obj); }
    if (!title.value) title.value = `${r.kind} ${r.name}`;
    if (!saveName.value) saveName.value = `${r.kind.toLowerCase()}-${r.name}`;
    loadSnaps();
  };

  const refresh = () => { loadProviders(); loadSnaps(); };
  return { el: host, refresh, useResource };
}
