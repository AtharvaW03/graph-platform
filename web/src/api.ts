import type {
  CallEdge,
  DependencyEdge,
  GlueJobInfo,
  HTTPRoute,
  ImpactNode,
  KafkaTopicInfo,
  PathNode,
  RepositoryOverview,
  SQLObjectInfo,
  SearchResult,
  SymbolResult,
} from "./types";

// In dev, requests go to /api and Vite's proxy (vite.config.ts) forwards them
// to the query-service, which sidesteps CORS entirely. In a production build,
// set VITE_API_BASE to the query-service's real origin.
//
// Deliberately NO auth token here: anything in a VITE_* variable is baked
// into the shipped JS bundle, so a token configured that way is readable by
// every browser that can load this page — it would silently turn the
// org-wide code index public. Deploy the UI behind a reverse proxy that
// injects the Authorization header server-side (or terminates SSO) instead.
const API_BASE = import.meta.env.VITE_API_BASE ?? "/api";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function get<T>(
  path: string,
  params?: Record<string, string | number | undefined>,
): Promise<T> {
  const url = new URL(API_BASE + path, window.location.origin);
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") url.searchParams.set(k, String(v));
    }
  }
  const res = await fetch(url.toString().replace(window.location.origin, ""));
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new ApiError(res.status, body || res.statusText);
  }
  return res.json() as Promise<T>;
}

async function post(path: string, body: unknown): Promise<void> {
  const res = await fetch(API_BASE + path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new ApiError(res.status, text || res.statusText);
  }
}

export interface FeedbackInput {
  endpoint: string;
  query: string;
  helpful: boolean;
  note?: string;
}

export const api = {
  search: (q: string) => get<SearchResult[]>("/search", { q }),
  findSymbol: (name: string) =>
    get<SymbolResult[]>(`/symbol/${encodeURIComponent(name)}`),
  findCallers: (symbol: string) =>
    get<CallEdge[]>(`/callers/${encodeURIComponent(symbol)}`),
  findCallees: (symbol: string) =>
    get<CallEdge[]>(`/callees/${encodeURIComponent(symbol)}`),
  blastRadius: (symbol: string, depth?: number) =>
    get<ImpactNode[]>(`/blast-radius/${encodeURIComponent(symbol)}`, { depth }),
  shortestPath: (src: string, dst: string) =>
    get<PathNode[]>("/path", { src, dst }),
  repositoryOverview: (repo: string) =>
    get<RepositoryOverview>(`/overview/${encodeURIComponent(repo)}`),
  findDependencies: (repo: string, scope?: string) =>
    get<DependencyEdge[]>(`/dependencies/${encodeURIComponent(repo)}`, {
      scope,
    }),
  findDependents: (dep: string) =>
    get<DependencyEdge[]>(`/dependents/${encodeURIComponent(dep)}`),
  findRoutes: (method?: string, path?: string, repo?: string) =>
    get<HTTPRoute[]>("/routes", { method, path, repo }),
  findKafkaTopic: (topic: string) =>
    get<KafkaTopicInfo>(`/kafka/topic/${encodeURIComponent(topic)}`),
  findSQLObject: (schema: string | undefined, name: string) =>
    get<SQLObjectInfo[]>("/sql/object", { schema, name }),
  findGlueJobs: (source?: string, target?: string) =>
    get<GlueJobInfo[]>("/glue/jobs", { source, target }),
  sendFeedback: (f: FeedbackInput) => post("/feedback", f),
};
