// JSON parse/stringify that keeps integers beyond 2^53 exact (device counters are
// often uint64). Such numbers are parsed into Num, which keeps the original text.

export class Num {
  constructor(public raw: string) {}
  toString() { return this.raw; }
}

export function parse(text: string): unknown {
  let i = 0;
  const ws = () => { while (i < text.length && " \t\n\r".includes(text[i])) i++; };
  const fail = (m: string): never => { throw new SyntaxError(`${m} at ${i}`); };
  const value = (): unknown => {
    ws();
    const c = text[i];
    if (c === "{") {
      i++;
      const o: Record<string, unknown> = {};
      ws();
      if (text[i] === "}") { i++; return o; }
      for (;;) {
        ws();
        if (text[i] !== '"') fail("expected key");
        const k = str();
        ws();
        if (text[i++] !== ":") fail("expected ':'");
        o[k] = value();
        ws();
        const d = text[i++];
        if (d === "}") return o;
        if (d !== ",") fail("expected ',' or '}'");
      }
    }
    if (c === "[") {
      i++;
      const a: unknown[] = [];
      ws();
      if (text[i] === "]") { i++; return a; }
      for (;;) {
        a.push(value());
        ws();
        const d = text[i++];
        if (d === "]") return a;
        if (d !== ",") fail("expected ',' or ']'");
      }
    }
    if (c === '"') return str();
    if (text.startsWith("true", i)) { i += 4; return true; }
    if (text.startsWith("false", i)) { i += 5; return false; }
    if (text.startsWith("null", i)) { i += 4; return null; }
    const m = /^-?\d+(\.\d+)?([eE][+-]?\d+)?/.exec(text.slice(i, i + 400));
    if (!m) fail("unexpected character");
    i += m![0].length;
    const n = Number(m![0]);
    if (!m![1] && !m![2] && !Number.isSafeInteger(n)) return new Num(m![0]);
    return n;
  };
  const str = (): string => {
    // find the closing quote, then let JSON.parse handle escapes
    let j = i + 1;
    while (j < text.length && text[j] !== '"') j += text[j] === "\\" ? 2 : 1;
    const s = JSON.parse(text.slice(i, j + 1));
    i = j + 1;
    return s;
  };
  const v = value();
  ws();
  if (i !== text.length) fail("trailing data");
  return v;
}

export function stringify(v: unknown, indent = 2): string {
  const pad = (d: number) => "\n" + " ".repeat(indent * d);
  const go = (x: unknown, d: number): string => {
    if (x instanceof Num) return x.raw;
    if (x === null || x === undefined) return "null";
    if (Array.isArray(x)) {
      if (x.length === 0) return "[]";
      return "[" + x.map((e) => (indent ? pad(d + 1) : "") + go(e, d + 1)).join(",") + (indent ? pad(d) : "") + "]";
    }
    if (typeof x === "object") {
      const ks = Object.keys(x as object);
      if (ks.length === 0) return "{}";
      return "{" + ks.map((k) => (indent ? pad(d + 1) : "") + JSON.stringify(k) + (indent ? ": " : ":") + go((x as any)[k], d + 1)).join(",") + (indent ? pad(d) : "") + "}";
    }
    return JSON.stringify(x);
  };
  return go(v, 0);
}
