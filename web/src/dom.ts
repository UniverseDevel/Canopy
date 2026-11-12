// Tiny DOM helpers. Text is always set with textContent, never innerHTML.

type Attrs = Record<string, string | number | boolean | ((e: any) => void) | undefined>;
type Child = Node | string | null | undefined | false;

export function h<K extends keyof HTMLElementTagNameMap>(tag: K, attrs: Attrs = {}, ...children: Child[]): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === false) continue;
    if (k.startsWith("on") && typeof v === "function") {
      el.addEventListener(k.slice(2).toLowerCase(), v as EventListener);
    } else if (k === "class") {
      el.className = String(v);
    } else if (k in el && typeof v !== "string") {
      (el as any)[k] = v;
    } else {
      el.setAttribute(k, v === true ? "" : String(v));
    }
  }
  append(el, ...children);
  return el;
}

export function append(el: Element, ...children: Child[]) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    el.append(typeof c === "string" ? document.createTextNode(c) : c);
  }
}

export function clear(el: Element) {
  while (el.firstChild) el.removeChild(el.firstChild);
}

export function $(sel: string): HTMLElement {
  const el = document.querySelector(sel);
  if (!el) throw new Error("missing " + sel);
  return el as HTMLElement;
}

let toastTimer = 0;
export function toast(msg: string, kind: "ok" | "err" = "ok") {
  let t = document.getElementById("toast");
  if (!t) {
    t = h("div", { id: "toast" });
    document.body.append(t);
  }
  t.textContent = msg;
  t.className = "show " + kind;
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (t!.className = ""), 2600);
}

export async function copyText(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`Copied ${what} (${text.length.toLocaleString()} characters)`);
  } catch {
    // Fallback for browsers that deny the clipboard API.
    const ta = h("textarea", { class: "offscreen" });
    ta.value = text;
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    toast(ok ? `Copied ${what}` : "Copy failed; the browser blocked the clipboard", ok ? "ok" : "err");
  }
}

export function field(label: string, input: HTMLElement, hint?: string) {
  return h("label", { class: "field" }, h("span", {}, label), input, hint ? h("small", {}, hint) : null);
}

// modal shows a dialog and resolves with the clicked button's value (null on
// Close or Escape). keepOpen handles a button inside the dialog (returns true
// to keep it open).
export function modal(title: string, body: HTMLElement, buttons: { label: string; primary?: boolean; value: string; danger?: boolean }[],
  opts: { wide?: boolean; keepOpen?: (value: string) => boolean | Promise<boolean> } = {}): Promise<string | null> {
  return new Promise((resolve) => {
    const close = (v: string | null) => { back.remove(); resolve(v); };
    const click = async (v: string) => {
      if (opts.keepOpen && (await opts.keepOpen(v))) return; // handled inside, stay open
      close(v);
    };
    const back = h("div", { class: "modal-back" },
      h("div", { class: "modal" + (opts.wide ? " wide" : "") }, h("h3", {}, title), body,
        h("div", { class: "row end" }, ...buttons.map((b) =>
          h("button", { class: (b.primary ? "primary" : "") + (b.danger ? " danger" : ""), "data-value": b.value, onclick: () => click(b.value) }, b.label)),
          h("button", { onclick: () => close(null) }, "Close"))));
    back.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Escape") close(null); });
    document.body.append(back);
    (back.querySelector("input, textarea, select") as HTMLElement | null ?? back.querySelector("button.primary") as HTMLElement | null)?.focus();
  });
}
