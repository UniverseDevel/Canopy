// Audit console: every operation the UI performed on a device, with the gNMIc
// (or kubectl) command that does the same. The server keeps the log, so it
// survives a page reload; it is also appended to audit.jsonl.

import { AuditEntry, auditApi } from "./api";
import { clear, copyText, h } from "./dom";
import { help } from "./help";

export interface ConsoleContext {
  shell(): string;
}

export function auditConsole(ctx: ConsoleContext) {
  const entries: AuditEntry[] = [];
  let since = 0;
  let file = "";
  let unseen = 0;
  const list = h("div", { class: "console-list" });
  // Newest entries are at the bottom. The list follows them while you are at the
  // bottom; when you have scrolled up to read, it stays put and offers a jump.
  let newBelow = 0;
  const jump = h("button", { class: "small console-jump", hidden: true, title: "Scroll to the newest entries" });
  const atBottom = () => list.scrollHeight - list.scrollTop - list.clientHeight < 24;
  const syncJump = () => {
    jump.hidden = newBelow === 0;
    jump.textContent = `${newBelow} new ↓`;
  };
  jump.addEventListener("click", () => { list.scrollTop = list.scrollHeight; newBelow = 0; syncJump(); });
  list.addEventListener("scroll", () => { if (newBelow && atBottom()) { newBelow = 0; syncJump(); } });
  const count = h("span", { class: "console-count" });
  const filter = h("input", { type: "search", placeholder: "Filter", class: "console-filter" });
  // Open by default; open/closed and height are remembered per browser.
  const pref = (k: string) => { try { return localStorage.getItem("canopy.console." + k); } catch { return null; } };
  const setPref = (k: string, v: string) => { try { localStorage.setItem("canopy.console." + k, v); } catch { /* private mode */ } };
  const grip = h("div", { class: "console-grip", title: "Drag to resize" });
  const el = h("div", { class: "console", hidden: pref("open") === "0" },
    grip,
    h("div", { class: "console-head" },
      h("strong", {}, "Console"),
      h("span", { class: "muted small" }, "Operations on devices and the equivalent commands"), help("console"),
      h("span", { class: "grow" }),
      filter,
      h("button", { class: "small", title: "Copy all commands", onclick: () => copyText(entries.map((e) => cmd(e)).filter(Boolean).join("\n"), "commands") }, "Copy commands"),
      h("button", { class: "small icon", title: "Close", onclick: () => toggle(false) }, "✕")),
    h("div", { class: "console-body" }, list, jump));
  const btn = h("button", { class: "link console-toggle", title: "Show the audit console", onclick: () => toggle() }, "Console ", count);

  const cmd = (e: AuditEntry) => e.command?.[ctx.shell()] ?? e.command?.bash ?? "";

  const row = (e: AuditEntry) => {
    const c = cmd(e);
    return h("div", { class: "centry" + (e.ok ? "" : " bad") },
      h("span", { class: "ctime muted" }, new Date(e.time).toLocaleTimeString()),
      h("span", { class: "cact act-" + e.action }, e.action),
      h("span", { class: "cdev" }, e.device ?? ""),
      h("span", { class: "csum", title: e.summary ?? "" }, e.summary ?? ""),
      h("span", { class: "cstat " + (e.ok ? "ok" : "err"), title: e.error ?? "" }, e.ok ? `ok · ${e.ms} ms` : `failed · ${e.ms} ms`),
      c ? h("code", { class: "ccmd", title: "Click to copy", onclick: () => copyText(c, "command") }, c) : null,
      !e.ok && e.error ? h("div", { class: "cerr err small" }, e.error) : null);
  };

  // draw renders the list; follow (default: whether you were at the bottom)
  // scrolls to the newest entry, added counts entries that arrived.
  const draw = (follow?: boolean, added = 0) => {
    const stick = follow ?? (atBottom() || el.hidden);
    const keep = list.scrollTop;
    const q = filter.value.toLowerCase();
    clear(list);
    const shown = entries.filter((e) => !q || `${e.action} ${e.device} ${e.summary} ${cmd(e)}`.toLowerCase().includes(q));
    if (!shown.length) list.append(h("p", { class: "muted small pad" }, entries.length ? "No matches." : "Nothing yet. Connects, reads and changes appear here."));
    let flashEl: HTMLElement | null = null;
    for (const e of shown) {
      const r = row(e);
      if (e.seq === flashSeq) { r.classList.add("flash"); flashEl = r; }
      list.append(r);
    }
    if (file) list.append(h("p", { class: "muted small pad" }, `Also written to ${file}`));
    if (stick) {
      list.scrollTop = list.scrollHeight;
      newBelow = 0;
    } else {
      list.scrollTop = keep;
      newBelow += added;
    }
    if (flashEl) { flashEl.scrollIntoView({ block: "nearest" }); flashSeq = 0; if (atBottom()) newBelow = 0; }
    syncJump();
    count.textContent = unseen ? `(${entries.length}, ${unseen} new)` : `(${entries.length})`;
  };

  const toggle = (open = el.hidden) => {
    el.hidden = !open;
    setPref("open", open ? "1" : "0");
    if (open) unseen = 0;
    draw(true);
  };

  const h0 = Number(pref("height"));
  if (h0 >= 80) el.style.height = h0 + "px";
  grip.addEventListener("pointerdown", (ev: PointerEvent) => {
    ev.preventDefault();
    const startY = ev.clientY, startH = el.getBoundingClientRect().height;
    document.body.classList.add("dragging-y");
    const move = (m: PointerEvent) => {
      const hgt = Math.max(80, Math.min(window.innerHeight * 0.75, startH + startY - m.clientY));
      el.style.height = hgt + "px";
    };
    const up = () => {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", up);
      document.body.classList.remove("dragging-y");
      setPref("height", String(Math.round(el.getBoundingClientRect().height)));
    };
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerup", up);
  });

  // A new failure: open the console, scroll to it and flash it (and the
  // footer button) so it is noticed.
  let flashSeq = 0;
  const flashFailure = (e: AuditEntry) => {
    flashSeq = e.seq;
    if (el.hidden) toggle(true);
    else draw();
    btn.classList.remove("flash");
    void btn.offsetWidth; // restart the animation
    btn.classList.add("flash");
  };
  const reveal = (seq?: number) => {
    if (el.hidden) toggle(true);
    const target = seq ?? [...entries].reverse().find((e) => !e.ok)?.seq;
    if (target) { flashSeq = target; draw(); }
  };

  let busy = false, first = true;
  const refresh = async () => {
    if (busy) return;
    busy = true;
    try {
      const r = await auditApi.list(since);
      file = r.file;
      let failed: AuditEntry | undefined;
      for (const e of r.entries) {
        entries.push(e);
        since = Math.max(since, e.seq);
        if (el.hidden && !first) unseen++;
        if (!first && !e.ok) failed = e;
      }
      first = false;
      if (entries.length > 1000) entries.splice(0, entries.length - 1000);
      if (failed) flashFailure(failed);
      else if (r.entries.length) draw(undefined, r.entries.length);
    } catch { /* server gone */ } finally { busy = false; }
  };
  filter.addEventListener("input", () => draw(true));
  // Requests finish before their audit entry is visible; fetch shortly after.
  window.addEventListener("canopy:audit", () => setTimeout(refresh, 150));
  refresh();
  return { el, btn, refresh, redraw: () => draw(), reveal };
}
