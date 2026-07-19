# A1 Knowledge Graph - project context

A knowledge graph over the org's codebases. The indexer clones every
configured repository, extracts a code graph (symbols, call edges, HTTP
routes, Kafka topics, SQL objects, Glue jobs, cross-repo dependencies) into
Neo4j, and keeps it fresh automatically. Three read surfaces sit on top: a
REST API (query-service), an MCP server so engineers' AI tools can query the
graph, and a web UI for engineers and non-engineers (PMs, security).

## Who uses it and for what

- Engineers: find symbols, trace callers/callees/blast radius from IDE AI
  tools (MCP) or the web UI, at near-zero marginal cost per query.
- PMs/PDs: assess which services a change touches; per-service overviews in
  plain language.
- Security/CISO: the full HTTP surface per repo with documented-vs-inferred
  classification, SQL objects, and "which repos use library X".

## Architecture

```
GitHub repos ──▶ indexer (clone → graphify AST extract → platform
                 extractors → import) ──▶ Neo4j
                    ▲            ▲
     push webhooks ─┘            └─ reconciliation sweep (periodic)

Neo4j ◀── query-service (REST :8080) ◀── mcp-server (stdio or HTTP :8090)
                    ▲                          ▲
                    └── web UI (nginx :80) ────┴── engineers' AI tools
```

- `cmd/indexer` - the pipeline daemon. One-shot, interval mode, or webhook
  mode (`--webhook-addr` + `--interval` as the reconciliation sweep).
- `cmd/query-service` - read-only REST API over the graph.
- `cmd/mcp-server` - MCP adapter exposing ~13 query tools. Stdio by default;
  `MCP_HTTP_ADDR` switches to a hosted streamable-HTTP endpoint many clients
  share.
- `cmd/importer` - manual one-off import of a static graph.json.
- `cmd/repogen` - one-off manifest generator from the GitHub org API.
- `internal/extract/*` - platform extractors (deps, httpapi, kafka, mssql,
  glue) that enrich graphify's AST graph.
- `web/` - React/Vite SPA served by nginx, which proxies `/api/*` to
  query-service and injects the auth token server-side (the browser never
  holds a token).

### Graph model

Everything is an `:Entity` keyed by `node_key`, with a specific label
(Function, Class, HttpRoute, KafkaTopic, SqlProcedure, GlueJob, ...). Code
entities are repo-owned (`repo` property, swept when they disappear from
source). Org-global entities (Kafka topics, packages, SQL objects,
repository hubs) are shared (`shared: true`, no repo): the same topic
referenced by five repos is one node, which is what makes cross-repo
traversal work. Every node/edge is stamped `last_commit` at import;
re-indexing sweeps what the new commit no longer contains.

`GraphSchemaVersion` (internal/index/orchestrator.go) governs migrations:
bump it whenever a change requires already-indexed repos to re-import
despite an unchanged HEAD (extractor behavior changes included). The
unchanged-HEAD skip only applies when the recorded version matches, so a
bump rolls out automatically on the next cycle.

## Load-bearing invariants (do not weaken these)

1. **Single writer.** Exactly one indexer instance may write to a database.
   A Neo4j-backed writer lease (TTL, heartbeat at ttl/4, `--steal-lease`
   for recovery) enforces it; a lost lease stops the daemon. Never deploy
   more than one indexer task.
2. **Fail closed on partial data.** An extractor error fails the whole repo
   (unless `extractors.allow_partial`), node-count mismatches and sweep
   residue fail the repo, and a failed repo's state does not advance -
   because importing a partial graph would let the sweep delete
   last-known-good data. Stale-but-correct beats fresh-but-wrong.
3. **Mass-retirement guard.** If more than half the graph's repos are
   missing from the manifest, nothing is deleted (assumed wrong config).
   Single-repo retirement: warning first, deletion only after a 1h grace
   period on a later full run. Targeted (`--repo`) runs never retire.
4. **Webhooks are the fast path, the sweep is the guarantee.** GitHub
   delivers webhooks at most once and never retries failures, so webhook
   mode requires `--interval` (reconciliation sweep) - the sweep bounds
   staleness regardless of lost deliveries. Do not make the sweep optional.
5. **No silent extraction drops.** A route (or any entity) that cannot be
   extracted must produce a fragment warning naming file/line/identifier.
   Silently missing data reads as false information to graph consumers;
   so does data invented from comments or test files (both are excluded).
6. **Secrets stay server-side.** The browser never receives tokens (nginx
   injects them); LLM keys (future) live only in backend env; tokens are
   compared constant-time.

## Auth model (three independent credentials)

- `QUERY_AUTH_TOKEN` - internal: web/nginx and mcp-server present it to
  query-service. Never leaves the deployment.
- `MCP_AUTH_TOKEN` - external: every engineer's MCP client config holds it.
  Widest-distributed secret; rotate first on any suspicion.
- `GITHUB_WEBHOOK_SECRET` - shared with GitHub; HMAC-verifies every
  delivery (X-Hub-Signature-256, constant-time).

They guard different boundaries and rotate independently.

## GitHub integration

- Auth is GitHub App only (org policy: no PATs). Env:
  `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`,
  `GITHUB_APP_PRIVATE_KEY_PATH`. Installation tokens are minted and
  refreshed automatically (50m cadence) into the git credential store.
- App permissions: Contents Read-only (+ mandatory Metadata). Webhook:
  Active, URL `https://<indexer host>/webhook/github`, Push event only.
- **Repository discovery** (`discovery.enabled: true` in the indexer
  config): the manifest becomes "every repo the App installation covers",
  TTL-cached (default 10m), merged with static `repositories:` entries
  (static wins by name, e.g. to pin a non-default branch). A failed refresh
  serves the last successful listing - a GitHub outage must never shrink
  the manifest (that would start retirement countdowns). Installing the
  App on a repo adds it to the graph; uninstalling retires it.

## Route extraction (internal/extract/httpapi) - subtleties

Line-oriented matchers across Go/Python/JS/TS/Java/Kotlin/C#/Ruby/PHP with:
- Go constant resolution: package consts (typed, raw-string, concatenation
  chains), file-scoped `:=` locals, `Group()` prefix chaining (single file),
  empty-path idiom (`group.POST("", h)` and empty-string constants) resolve
  to the group's own path.
- Formatter-wrapped (multi-line) registrations are joined and re-matched.
- Comments are stripped before matching in every language (commented-out
  routes must not enter the graph).
- Test files (`_test.go`, `*.test.ts`, `testdata/`, `mocks/`) are excluded.
- OpenAPI/Swagger specs are ingested (own 10MB cap, loud skip warnings) and
  reconciled with code routes: exact method+path match with path parameters
  canonicalized (`:id` == `{id}` == `<id>`), spec wins and marks
  `documented: true`. Spec-only routes legitimately remain (documented but
  not implemented) - that drift is signal, not noise.
- Known limitation: constants imported from another repo/module do not
  resolve (per-repo scan); such routes are dropped with a warning.

## Freshness

- Webhook mode re-indexes pushed repos within seconds; the sweep re-checks
  everything every `--interval` (30m recommended; NFR is <1h worst case).
- The indexer stamps `last_synced_at` / `last_indexed_at` on each
  `(:Repository)` node; query-service serves `GET /freshness` (per-repo
  ages + overall stale verdict at 1h); the web UI footer shows "Updated Xm
  ago" and warns when stale. `/freshness` `stale: true` is a ready-made
  alarm condition.

## Observability

- All services log plainly to stderr/stdout (captured by the platform's
  log infrastructure in deployment).
- `internal/httpmw.WithRequestLog` logs method/path/status/duration/bytes
  on query-service, hosted mcp-server, and the indexer webhook server.
  Health probes excluded; query strings never logged.
- Indexer webhook mode also serves `GET /status` (bearer
  `INDEXER_STATUS_TOKEN`): per-repo last-indexed commit, failures, pending
  re-indexes.
- The web UI has a feedback widget posting to `POST /feedback` (per-query
  helpful/unhelpful ratings; `GET /feedback/stats`).

## graphify (the AST extractor dependency)

- External pinned tool: PyPI package `graphifyy` (CLI `graphify`), invoked
  as `graphify extract {repo_path} --code-only --force`. `--code-only` =
  local AST only, no LLM. `--force` is required (purges excluded files,
  bypasses the anti-shrink guard so legitimate shrinks import correctly).
- Version discipline: the Docker image pins an exact version and the config
  `expected_version` hard-fails the run on mismatch. Keep pin, config, and
  docs aligned when bumping. Upstream releases frequently; only
  integrity-class fixes justify mid-cycle bumps.
- Timing expectations: a typical microservice indexes in ~10-30s; a
  monorepo-class repo (~20k files) takes ~30 min per pass and needs
  `graphify.timeout: 60m` (default 20m). The repo loop is sequential, so
  one huge repo delays the rest of its cycle - parallelizing the loop is a
  known, scoped follow-up if that becomes a problem.

## Web UI conventions

Navigation is grouped by task (Find / Understand / Review) and each page
header repeats its group as an eyebrow. Plain language over jargon
("What breaks?" over "blast radius" - the technical term lives in hover
hints; "code elements" over "nodes"; "Used by" over "fan-in"). Modes are
visible segmented controls, never dropdowns. Identifier lists render as
capped mono chips; tabular data as real tables. Both themes (light/dark)
are maintained; no external webfonts.

## Testing

- `go test ./...` - unit + in-process suites, no external deps.
- Integration suites (internal/neo4j, internal/query) run only when
  `NEO4J_TEST_URI` (+ `NEO4J_TEST_PASSWORD`) point at a real Neo4j and skip
  silently otherwise. Run packages sequentially (`-p 1`) against a shared
  test database - the suites are not isolated across packages.
- Web: `npm run build` (includes tsc) and `npm run lint` (oxlint). No
  component tests yet.
- Load-test method (autocannon at the MCP endpoint) is documented in the
  README-adjacent runbooks; latest measured: search p75 21-30ms at 100 RPS
  on 14 repos / ~674k entities, flat at 300 RPS, 0 failures (budget: 5s).

## Deployment

Four images (deploy/ + web/Dockerfile), compose file is local-only. Prod
needs: ALB + TLS + DNS in front of web, mcp-server (`/mcp`), and the
indexer (`/webhook/github`); health checks on `/health`; indexer as exactly
one task; Neo4j with a persistent volume and page cache sized larger than
the store (undersizing it is the likeliest way to lose the latency NFR).
Secrets via the org's secrets manager: `NEO4J_PASSWORD`,
`QUERY_AUTH_TOKEN`, `MCP_AUTH_TOKEN`, `GITHUB_WEBHOOK_SECRET`,
`GITHUB_APP_*` (the .pem is written to a path by the entrypoint). The
GitHub App's webhook URL is filled in after deployment assigns the host.
Alarms worth having: indexer task count < 1, ALB 5xx rate, `/freshness`
stale.

## Working conventions

- Extractor output changes require a `GraphSchemaVersion` bump.
- Prefer failing a repo over importing questionable data (see invariants).
- Config files named `*.local.yaml` are gitignored operator configs.
