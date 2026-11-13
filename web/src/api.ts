// Typed client for the Canopy JSON API. Every call carries the session token.

import { parse, stringify } from "./json";

export interface AppError {
  code: string;
  message: string;
  hint?: string;
  grpcCode?: string;
  details?: Record<string, string>;
}

export class ApiError extends Error {
  constructor(public err: AppError, public status: number, public body?: any) {
    super(err.message);
  }
}

export interface Model { name: string; organization?: string; version?: string }
export interface Capabilities { gnmiVersion: string; encodings: string[]; models: Model[] }
export interface Session {
  id: string; label: string; preset?: string; yangDirs?: string[]; paths?: string[]; models?: string;
  address: string; jumps?: string[]; hops?: string[];
  capabilities: Capabilities; encoding: string; connectMs: number;
  device?: { namespace: string; name: string; provider: string };
}

export interface Preset {
  name: string; description?: string; tags?: string[];
  address: string; insecure?: boolean; skipVerify?: boolean; caFile?: string; certFile?: string; keyFile?: string;
  serverName?: string; username?: string; passwordEnv?: string; timeoutSec?: number;
  jumps?: string[]; identityFiles?: string[]; encoding?: string; yangDirs?: string[]; paths?: string[]; models?: string;
  lastSeen?: { time: string; gnmiVersion?: string; encodings?: string[]; models: number };
  source?: string; readOnly?: boolean;
}

export interface TypeInfo {
  name: string; base: string; range?: string; length?: string; patterns?: string[];
  enums?: string[]; bits?: string[]; identityBase?: string; leafref?: string;
  fractionDigits?: number; union?: TypeInfo[];
}

export interface SNode {
  id: number; parent: number; name: string; kind: string; module: string; prefix?: string;
  path: string; qpath: string; config: boolean; keys?: string[]; hasChildren: boolean;
  childCount: number; type?: TypeInfo; description?: string; units?: string; default?: string[];
  mandatory?: boolean; presence?: boolean; choice?: string; location?: string; score?: number;
}

export interface SchemaSummary {
  dir: string; files: number; nodes: number; roots: number; loadMs: number; warnings?: string[];
  modules: { name: string; revision?: string; prefix?: string; version?: string; topLevel: number; file?: string }[];
}

export interface NodeDetail {
  node: SNode; ancestors: number[]; source?: string; sourceFile?: string; sourceLine?: number; sourceError?: string;
}

export interface Update { path: string; value: unknown }
export interface GetResult {
  paths: string[]; type: string; encoding: string; data: unknown; flat: Update[];
  notifications: { timestamp: number; updates?: Update[]; deletes?: string[] }[];
  empty: boolean; elapsedMs: number; sizeBytes: number; leafCount: number; updateCount: number;
}

export interface Target {
  address: string; insecure?: boolean; skipVerify?: boolean; caFile?: string; certFile?: string; keyFile?: string;
  serverName?: string; username?: string; password?: string; timeoutSec?: number;
  jumps?: string[]; identityFiles?: string[]; passphrases?: Record<string, string>; sshPassword?: string;
  acceptHostKeys?: string[]; saveHostKeys?: boolean; preset?: string;
}

export interface GenCommand { title: string; text: string; note?: string }
export interface GenOutput { shell: string; commands: GenCommand[] }

const token = new URLSearchParams(location.search).get("token") ?? "";

async function call<T>(method: string, url: string, body?: unknown, exact = false): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: { "X-Canopy-Token": token, ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: body !== undefined ? stringify(body, 0) : undefined,
  });
  const text = await res.text();
  let data: any = null;
  try { data = text ? (exact ? parse(text) : JSON.parse(text)) : null; } catch { /* not JSON */ }
  // Anything that may have reached a device is audited; tell the console.
  if (method !== "GET" && !/^\/api\/(format|generate|models\/search)/.test(url)) {
    window.dispatchEvent(new Event("canopy:audit"));
  }
  if (!res.ok) {
    throw new ApiError(data?.error ?? { code: "http_" + res.status, message: text || res.statusText }, res.status, data);
  }
  return data as T;
}

export const api = {
  info: () => call<{ build: { version: string; commit: string } }>("GET", "/api/info"),
  connect: (t: Target) => call<Session>("POST", "/api/connect", t),
  disconnect: (session: string) => call("POST", "/api/disconnect", { session }),
  sessions: () => call<Session[]>("GET", "/api/sessions"),
  get: (session: string, paths: string[], type: string, encoding: string) =>
    call<GetResult>("POST", "/api/get", { session, paths, type, encoding }, true),
  generate: (req: Record<string, unknown>) => call<GenOutput>("POST", "/api/generate", req),
  loadSchema: (dirs: string[], force = false) => call<SchemaSummary>("POST", "/api/schema/load", { dirs, force }),
  presets: () => call<Preset[]>("GET", "/api/connections"),
  savePreset: (p: Preset, keychain?: { password?: string; rememberPassword?: boolean; forgetPassword?: boolean }) =>
    call<Preset>("PUT", "/api/connections", { ...p, ...(keychain ?? {}) }),
  deletePreset: (name: string) => call("DELETE", `/api/connections?name=${encodeURIComponent(name)}`),
  schema: () => call<SchemaSummary>("GET", "/api/schema"),
  children: (id: number) => call<SNode[]>("GET", `/api/schema/children?id=${id}`),
  node: (id: number) => call<NodeDetail>("GET", `/api/schema/node?id=${id}`),
  search: (q: string) => call<SNode[]>("GET", `/api/schema/search?q=${encodeURIComponent(q)}`),
  find: (path: string) => call<{ node: SNode; ancestors: number[] }>("GET", `/api/schema/find?path=${encodeURIComponent(path)}`),
  format: (format: "yaml" | "json", data: unknown) => call<{ text: string }>("POST", "/api/format", { format, data }),
};

export interface ModelSource { id: string; name: string; repo: string; description?: string }
export interface ModelVersion { name: string; cached: boolean }
export interface Detection { profile?: string; vendor?: string; os?: string; provider?: string; source?: string; deviceVersion?: string; want?: string; version?: string; exact: boolean; reason: string }
export interface ModelResult { detection?: Detection; set?: { source: string; version: string; dirs: string[]; cached: boolean }; schema?: SchemaSummary; error?: AppError }

export const modelsApi = {
  sources: () => call<ModelSource[]>("GET", "/api/models/sources"),
  versions: (source: string, refresh = false) => call<ModelVersion[]>("GET", `/api/models/versions?source=${encodeURIComponent(source)}${refresh ? "&refresh=1" : ""}`),
  load: (source: string, version: string) => call<ModelResult>("POST", "/api/models/load", { source, version }),
  auto: (session: string) => call<ModelResult>("POST", "/api/models/auto", { session }),
};

export interface CatalogEntry {
  source: string; version: string; path: string; module: string; kind?: string; revision?: string; namespace?: string;
  prefix?: string; ocVersion?: string; belongsTo?: string; description?: string; imports?: string[]; deviates?: string[]; enriched: boolean;
}
export interface CatalogRelease { source: string; version: string; files: number; enriched: boolean; indexedAt: string }
export interface CatalogQuery { q?: string; source?: string; version?: string; module?: string; limit?: number }
export interface DeviceModelsResult { dir: string; files: number; skipped?: string[]; cached: boolean; schema?: SchemaSummary }
export const catalogApi = {
  fromDevice: (req: { session: string; port?: number; host?: string; acceptHostKeys?: string[]; saveHostKeys?: boolean; refresh?: boolean }) =>
    call<DeviceModelsResult>("POST", "/api/models/device", req),
  releases: () => call<CatalogRelease[]>("GET", "/api/models/catalog"),
  search: (q: CatalogQuery) => call<CatalogEntry[]>("POST", "/api/models/search", q),
  index: (source: string, version: string) => call<{ source: string; version: string; files: number; enriched: boolean }>("POST", "/api/models/index", { source, version }),
};

export interface MatchEntry { module: string; status: string; advertised?: string; loaded?: string; file?: string }
export interface MatchReport { counts: Record<string, number>; entries: MatchEntry[] }
export const reportApi = {
  get: (session: string) => call<MatchReport>("GET", `/api/models/report?session=${encodeURIComponent(session)}`),
};

export interface SetOp { op: string; path: string; value?: unknown }
export interface DiffChange { path: string; kind: string; before?: unknown; after?: unknown }
export interface DiffResult { added: number; removed: number; changed: number; unchanged: number; changes: DiffChange[] }
export interface SetOutcome {
  preview: string; gnmic: GenCommand[]; problems: string[]; destructive: boolean; sent: boolean;
  result?: { elapsedMs: number }; diff?: DiffResult;
}
export interface SnapMeta { name: string; device: string; address: string; time: string; paths: string[]; type: string; leaves: number; note?: string; models?: string }

export const configApi = {
  set: (body: { session: string; ops: SetOp[]; dryRun?: boolean; confirm?: boolean; shell?: string; ignore?: string[] }) =>
    call<SetOutcome>("POST", "/api/set", body),
  skeleton: (id: number) => call<{ value: unknown }>("GET", `/api/schema/skeleton?id=${id}`, undefined, true),
  current: (session: string, path: string) =>
    call<GetResult>("POST", "/api/get", { session, paths: [path], type: "config", encoding: "" }, true),
};

export const snapApi = {
  list: () => call<SnapMeta[]>("GET", "/api/snapshots"),
  take: (session: string, paths: string[], type: string, name?: string, note?: string) =>
    call<SnapMeta>("POST", "/api/snapshots", { session, paths, type, name, note }),
  diff: (a: string, b: string, session: string, ignore: string[]) =>
    call<DiffResult>("POST", "/api/snapshots/diff", { a, b, session, ignore }, true),
  remove: (name: string) => call("DELETE", `/api/snapshots?name=${encodeURIComponent(name)}`),
};

export interface OpOptions { kubeconfig?: string; context?: string; jumps?: string[]; identityFiles?: string[]; operatorRepo?: string; timeoutSec?: number }
export interface Condition { type: string; status: string; reason?: string; message?: string }
export interface K8sDevice { namespace: string; name: string; provider: string; address: string; phase?: string; paused: boolean; ready: string; conditions: Condition[]; tls: boolean }
export interface K8sResource { kind: string; resource: string; version?: string; namespace: string; name: string; ready: string; paused: boolean; conditions: Condition[]; spec?: Record<string, unknown>; generation: number; observedGeneration: number }
export interface VerifyLeaf { path: string; expected?: unknown; actual?: unknown; status: string; from?: string }
export interface Verification { resource: K8sResource; device: string; fixture: string; mode: string; verdict: string; reasons: string[]; leaves: VerifyLeaf[]; alternates?: string[]; elapsedMs: number }

export const opApi = {
  info: () => call<{ defaults: OpOptions; contexts?: string[]; current?: string; error?: string }>("GET", "/api/operator/info"),
  devices: (options: OpOptions, namespace = "") => call<{ context: string; devices: K8sDevice[] }>("POST", "/api/operator/devices", { options, namespace }),
  connect: (options: OpOptions, namespace: string, name: string, target: Partial<Target>) =>
    call<Session>("POST", "/api/operator/connect", { options, namespace, name, target }),
  resources: (options: OpOptions, namespace: string, device: string) =>
    call<K8sResource[]>("POST", "/api/operator/resources", { options, namespace, device }),
  pause: (options: OpOptions, kind: string, namespace: string, name: string, paused: boolean) =>
    call<{ paused: boolean; condition: string }>("POST", "/api/operator/pause", { options, kind, namespace, name, paused, waitSec: 30 }),
  verify: (options: OpOptions, session: string, kind: string, namespace: string, name: string, deleted = false) =>
    call<Verification>("POST", "/api/operator/verify", { options, session, kind, namespace, name, deleted }, true),
};

export interface AuditEntry {
  seq: number; time: string; session?: string; device?: string; action: string; summary?: string;
  command?: Record<string, string>; ok: boolean; error?: string; ms: number;
}
export const auditApi = {
  list: (since: number) => call<{ entries: AuditEntry[]; file: string }>("GET", `/api/audit?since=${since}`),
};

export interface FixtureInfo { file: string; title: string; kinds: string[] }
export interface FixtureStep { time: string; step: string; ok: boolean; detail?: string }
export interface FixtureRun {
  fixture: string; title: string; namespace: string; verdict: string; steps: FixtureStep[];
  diff?: { path: string; kind: string; before?: unknown; after?: unknown }[]; elapsedMs: number;
}
export interface FixtureRunOptions {
  provider: string; file?: string; text?: string; deviceAddress: string; shadowControl?: string; shadowListen?: string;
  tls: boolean; username?: string; password?: string; timeoutSec?: number; keep?: boolean;
}
export const fixturesApi = {
  list: (options: OpOptions, provider = "") =>
    call<{ repo: string; dir?: string; providers: string[]; fixtures: FixtureInfo[] }>("POST", "/api/fixtures/list", { options, provider }),
  read: (options: OpOptions, provider: string, file: string) => call<{ text: string }>("POST", "/api/fixtures/read", { options, provider, file }),
  export: (b: { title: string; cr: string; preload?: string; expect: string; delete?: string }) => call<{ text: string }>("POST", "/api/fixtures/export", b),
  save: (options: OpOptions, provider: string, name: string, text: string) => call<{ file: string }>("POST", "/api/fixtures/save", { options, provider, name, text }),
  run: (options: OpOptions, run: FixtureRunOptions) => call<FixtureRun>("POST", "/api/fixtures/run", { options, run }, true),
};
