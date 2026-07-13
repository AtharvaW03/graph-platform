import type {
  CallEdge,
  DependencyEdge,
  GlueJobInfo,
  HTTPRoute,
  HotspotNode,
  ImpactNode,
  KafkaTopicInfo,
  PathNode,
  RepoInfo,
  RepositoryOverview,
  SQLObjectInfo,
  SearchResult,
  SymbolResult,
} from "./types";

// scopeParam serializes the global repo scope for the `repos=` query
// parameter; undefined (param omitted) when the scope is empty.
function scopeParam(repos?: string[]): string | undefined {
  return repos && repos.length > 0 ? repos.join(",") : undefined;
}

// In dev, requests go to /api and Vite's proxy (vite.config.ts) forwards them
// to the query-service, which sidesteps CORS entirely. In a production build,
// set VITE_API_BASE to the query-service's real origin.
//
// Deliberately NO auth token here: anything in a VITE_* variable is baked
// into the shipped JS bundle, so a token configured that way is readable by
// every browser that can load this page - it would silently turn the
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
  // /health and /ready are unauthenticated (see internal/api/server.go), so
  // neither needs the token the rest of this client deliberately doesn't
  // have. /health is process liveness only; /ready also pings Neo4j, which
  // is what "is the graph actually queryable" means.
  health: () => get<{ status: string }>("/health"),
  ready: () => get<{ status: string }>("/ready"),
  listRepos: () => get<RepoInfo[]>("/repos"),
  search: (q: string, repos?: string[]) =>
    get<SearchResult[]>("/search", { q, repos: scopeParam(repos) }),
  findSymbol: (name: string, repos?: string[]) =>
    get<SymbolResult[]>(`/symbol/${encodeURIComponent(name)}`, {
      repos: scopeParam(repos),
    }),
  findCallers: (symbol: string, repos?: string[]) =>
    get<CallEdge[]>(`/callers/${encodeURIComponent(symbol)}`, {
      repos: scopeParam(repos),
    }),
  findCallees: (symbol: string, repos?: string[]) =>
    get<CallEdge[]>(`/callees/${encodeURIComponent(symbol)}`, {
      repos: scopeParam(repos),
    }),
  blastRadius: (symbol: string, depth?: number, repos?: string[]) =>
    get<ImpactNode[]>(`/blast-radius/${encodeURIComponent(symbol)}`, {
      depth,
      repos: scopeParam(repos),
    }),
  shortestPath: (src: string, dst: string, repos?: string[]) =>
    get<PathNode[]>("/path", { src, dst, repos: scopeParam(repos) }),
  repositoryOverview: (repo: string) =>
    get<RepositoryOverview>(`/overview/${encodeURIComponent(repo)}`),
  findDependencies: (repo: string, scope?: string) =>
    get<DependencyEdge[]>(`/dependencies/${encodeURIComponent(repo)}`, {
      scope,
    }),
  findDependents: (dep: string) =>
    get<DependencyEdge[]>(`/dependents/${encodeURIComponent(dep)}`),
  findRoutes: (method?: string, path?: string, repos?: string[]) =>
    get<HTTPRoute[]>("/routes", { method, path, repos: scopeParam(repos) }),
  findKafkaTopic: (topic: string) =>
    get<KafkaTopicInfo>(`/kafka/topic/${encodeURIComponent(topic)}`),
  findSQLObject: (schema: string | undefined, name: string) =>
    get<SQLObjectInfo[]>("/sql/object", { schema, name }),
  findGlueJobs: (source?: string, target?: string) =>
    get<GlueJobInfo[]>("/glue/jobs", { source, target }),
  findHotspots: (repos?: string[], limit?: number) =>
    get<HotspotNode[]>("/hotspots", { repos: scopeParam(repos), limit }),
  sendFeedback: (f: FeedbackInput) => post("/feedback", f),
};
