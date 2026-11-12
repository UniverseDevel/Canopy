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
  constructor(public err: AppError, public status: number) {
    super(err.message);
  }
}

export interface Model { name: string; organization?: string; version?: string }
export interface Capabilities { gnmiVersion: string; encodings: string[]; models: Model[] }
export interface Session {
  id: string; address: string; jumps?: string[]; hops?: string[];
  capabilities: Capabilities; encoding: string; connectMs: number;
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
  acceptHostKeys?: string[]; saveHostKeys?: boolean;
}

export interface GenCommand { title: string; text: string; note?: string }
export interface GenOutput { shell: string; commands: GenCommand[]; config?: string }

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
  if (!res.ok) {
    throw new ApiError(data?.error ?? { code: "http_" + res.status, message: text || res.statusText }, res.status);
  }
  return data as T;
}

export const api = {
  info: () => call<{ build: { version: string; commit: string } }>("GET", "/api/info"),
  connect: (t: Target) => call<Session>("POST", "/api/connect", t),
  disconnect: (session: string) => call("POST", "/api/disconnect", { session }),
  get: (session: string, paths: string[], type: string, encoding: string) =>
    call<GetResult>("POST", "/api/get", { session, paths, type, encoding }, true),
  generate: (req: Record<string, unknown>) => call<GenOutput>("POST", "/api/generate", req),
  loadSchema: (dirs: string[]) => call<SchemaSummary>("POST", "/api/schema/load", { dirs }),
  schema: () => call<SchemaSummary>("GET", "/api/schema"),
  children: (id: number) => call<SNode[]>("GET", `/api/schema/children?id=${id}`),
  node: (id: number) => call<NodeDetail>("GET", `/api/schema/node?id=${id}`),
  search: (q: string) => call<SNode[]>("GET", `/api/schema/search?q=${encodeURIComponent(q)}`),
  find: (path: string) => call<{ node: SNode; ancestors: number[] }>("GET", `/api/schema/find?path=${encodeURIComponent(path)}`),
  format: (format: "yaml" | "json", data: unknown) => call<{ text: string }>("POST", "/api/format", { format, data }),
};
