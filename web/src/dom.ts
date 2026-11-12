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
