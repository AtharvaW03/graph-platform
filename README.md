# code-intel - Org-Wide Code Intelligence Platform

A self-serve code intelligence service: it keeps every configured repository
cloned and indexed into a Neo4j knowledge graph, and answers questions like
*"where is `processPayment` defined?"*, *"which repos depend on
auth-service?"*, *"who consumes the `trade_executed` topic?"*, or *"which
procs write to the trades table?"* - across repositories and languages.

> The three documents under [`docs/graphify/`](docs/graphify/) describe
> **graphify**, the external AST-extraction CLI this platform shells out to.
> They are reference material for that tool, not documentation of this repo.

## Architecture

```
config/repos.yaml
      │
      ▼
┌──────────────┐   clone/fetch   ┌────────────┐
│   indexer    │────────────────▶│  workdir/  │  (shallow clones + state.json)
│ (cmd/indexer)│                 └────────────┘
│              │  1. git sync
│              │  2. graphify extract <repo> --code-only --force → graphify-out/graph.json (AST)
│              │  3. extractors (deps, http_api, kafka, mssql, glue)
│              │  4. merge fragments into graph.json
│              │  5. import into Neo4j + sweep stale + verify counts
└──────┬───────┘
       ▼
   ┌───────┐     ┌───────────────┐     ┌────────────┐
   │ Neo4j │◀────│ query-service │◀────│ mcp-server │◀── IDE / AI agents
   └───────┘     │  (REST, 8080) │     │  (stdio)   │
                 └───────▲───────┘     └────────────┘
                         └── engineers via curl / web UI
```

- **`cmd/indexer`**: the pipeline daemon. One-shot, continuous
  (`--interval 15m`), or webhook-driven (`--webhook-addr` - re-indexes on
  GitHub push deliveries, with `--interval` as the reconciliation sweep; see
  "Webhook-triggered indexing" below). Per-repo failures never block other
  repos; state persists in `workdir/state.json`.
- **`cmd/query-service`**: read-only REST API over the graph.
- **`cmd/mcp-server`**: MCP adapter exposing the query API as agent tools
  (`search_code`, `find_callers`, `blast_radius`, `repository_overview`,
  ...). Speaks stdio by default (local subprocess per user); set
  `MCP_HTTP_ADDR` to instead serve the MCP streamable HTTP transport as one
  hosted endpoint many clients share - see "Remote MCP access" below.
- **`cmd/importer`**: manual one-off import of a static `graph.json`.
- **`internal/extract/*`**: domain extractors that enrich the AST graph:
  dependency manifests, HTTP routes, Kafka topics, MS SQL
  objects, AWS Glue jobs.

### Graph model

Everything is an `:Entity` node keyed by `node_key`, with a specific label
(`Function`, `Class`, `HttpRoute`, `KafkaTopic`, `SqlProcedure`, `GlueJob`,
...). Code entities are **repo-owned** (`repo` property, swept when they
disappear from the source). Org-global entities: Kafka topics, packages, SQL
objects, repository hubs are **shared** (`shared: true`, no `repo`
property): the same topic referenced by five repos is ONE node, which is what
makes cross-repo traversal work. Shared nodes are garbage-collected only when
no repo references them anymore.

Every node and edge is stamped with `last_commit` at import; re-indexing a
repo sweeps whatever the new commit no longer contains.

Terraform resources flow through graphify as generic AST nodes - there's no
dedicated label mapping for them yet, so `InferLabel`'s existing heuristics
bucket them same as any other file-derived symbol.

The indexer appends platform-wide exclusions (`graphify.ignore_patterns`,
default `*.tfvars`) into every checkout's `.graphifyignore` before extraction,
because graphify only reads ignore files from the repo being scanned - a
pattern committed here protects nothing but this repo. Repo-owned ignore
entries are preserved; tfvars stay out of the graph everywhere by default.

Graph-model changes carry a schema version (`GraphSchemaVersion`): when it
bumps, every repo re-indexes on its next cycle even with an unchanged HEAD,
so migrations roll out without anyone remembering `--force`.

## Quickstart

Prerequisites: Go 1.26+, Docker Desktop, a local `git`, Neo4j 5.x, and the
`graphify` CLI (pinned to 0.9.13; install the terraform extra for `.tf`/`.hcl`
coverage: `uv tool install "graphifyy[terraform]"`) on PATH.

```powershell
# 1. Neo4j (e.g. Docker)
docker run -d --name neo4j -p 7474:7474 -p 7687:7687 -e NEO4J_AUTH=neo4j/<your-password> neo4j:5

# 2. Environment
$env:NEO4J_PASSWORD = "<your-password>"      # required by indexer + query-service
$env:QUERY_AUTH_TOKEN = "<token>"    # optional; omit for open local dev

# 3. Index everything in config/repos.yaml
go run ./cmd/indexer --all                  # one-shot
go run ./cmd/indexer --all --interval 15m   # continuous daemon

# 4. Serve queries
go run ./cmd/query-service                  # listens on :8080

# 5. Ask questions
curl -H "Authorization: Bearer dev-token" "http://localhost:8080/search?q=processPayment"
curl -H "Authorization: Bearer dev-token" "http://localhost:8080/overview/my-repo"
```

### Environment variables

| Variable | Used by | Default | Purpose |
|---|---|---|---|
| `NEO4J_PASSWORD` | indexer, query-service, importer | *(required)* | Neo4j auth |
| `NEO4J_URI` | indexer, query-service | `neo4j://127.0.0.1:7687` | Neo4j endpoint |
| `NEO4J_USER` | indexer, query-service | `neo4j` | Neo4j user |
| `QUERY_PORT` | query-service | `8080` | listen port |
| `QUERY_BIND` | query-service | `127.0.0.1` without auth, all interfaces with | listen address |
| `QUERY_AUTH_TOKEN` | query-service, mcp-server | *(empty = no auth)* | static bearer token |
| `QUERY_CORS_ORIGIN` | query-service | *(empty = CORS disabled)* | single trusted origin for a cross-origin web UI |
| `QUERY_SERVICE_URL` | mcp-server | `http://localhost:8080` | query-service base URL |
| `QUERY_TIMEOUT` | mcp-server | `30s` | per-request timeout |
| `MCP_HTTP_ADDR` | mcp-server | *(empty = stdio)* | serve MCP over HTTP on this address instead of stdio (e.g. `0.0.0.0:8090`) |
| `MCP_AUTH_TOKEN` | mcp-server | *(required in HTTP mode on a non-loopback address)* | bearer token required on incoming MCP connections in HTTP mode; separate from `QUERY_AUTH_TOKEN` |
| `MCP_ALLOW_NO_AUTH` | mcp-server | *(empty)* | set to `1` to allow HTTP mode with no `MCP_AUTH_TOKEN` on a reachable address; otherwise the server fails closed |
| `GITHUB_WEBHOOK_SECRET` | indexer | *(required with `--webhook-addr`)* | HMAC secret every webhook delivery must be signed with (`X-Hub-Signature-256`); same value entered in the GitHub webhook config |
| `INDEXER_STATUS_TOKEN` | indexer | *(empty = no auth)* | bearer token for the indexer's `/status` endpoint in webhook mode |
| `GIT_TOKEN` | indexer | *(empty = public repos only)* | PAT for cloning private repos over HTTPS; ignored if the `GITHUB_APP_*` vars below are set |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY_PATH` | indexer | *(empty = fall back to `GIT_TOKEN`)* | GitHub App auth, all three required together; short-lived installation tokens instead of a long-lived PAT, minted at startup and refreshed automatically before each hourly expiry |

### REST endpoints

All JSON. With `QUERY_AUTH_TOKEN` set, every endpoint except `/health` and
`/ready` requires `Authorization: Bearer <token>`. All read-only except
`/feedback`.

Symbol-level endpoints (`/search`, `/symbol`, `/callers`, `/callees`,
`/blast-radius`, `/path`, `/routes`, `/hotspots`) accept an optional
`repos=` filter (comma-separated repository names; empty = all).

| Endpoint | Answers |
|---|---|
| `GET /repos` | indexed repositories with node counts |
| `GET /search?q=` | fuzzy symbol search, ranked exact > prefix > contains |
| `GET /symbol/{name}` | every definition of a symbol, all repos |
| `GET /callers/{symbol}` · `/callees/{symbol}` | direct call edges |
| `GET /blast-radius/{symbol}?depth=` | reachable nodes with distance |
| `GET /path?src=&dst=` | shortest path between two symbols |
| `GET /overview/{repo}` | structured onboarding snapshot for one repo |
| `GET /dependencies/{repo}?scope=` · `/dependents/{dep}` | manifest dependency edges |
| `GET /routes?method=&path=&repo=` | HTTP API inventory |
| `GET /kafka/topic/{name}` | producers/consumers of a topic, all repos |
| `GET /sql/object?schema=&name=` | SQL object + reads/writes/dependencies |
| `GET /glue/jobs?source=&target=` | Glue jobs by source/target table |
| `GET /hotspots?repo=&limit=` | entities ranked by incoming dependency fan-in |
| `GET /freshness` | per-repository last-checked/last-indexed stamps and an overall stale verdict (>1h) |
| `POST /feedback` · `GET /feedback/stats?days=` | relevance ratings + the quality-metric rollup |

## Running the full stack

Four processes, run manually (see Quickstart above for env vars):

```bash
# 1. Neo4j - Docker one-liner or Neo4j Desktop
docker run -d --name neo4j -p 7474:7474 -p 7687:7687 -e NEO4J_AUTH=neo4j/<pw> neo4j:5

# 2. Indexer, continuous
go run ./cmd/indexer --all --interval 1h

# 3. Query API
go run ./cmd/query-service

# 4. Web UI (dev server proxies /api -> :8080; export QUERY_AUTH_TOKEN too
#    if the query-service runs with one - the proxy injects it server-side)
cd web && npm install && npm run dev     # http://localhost:5173
```

For a shared/pilot box, run the same processes under a supervisor (systemd,
launchd, pm2) and put the web UI behind any reverse proxy that forwards
`/api` with the Authorization header.

## Remote MCP access

By default each user runs mcp-server as a local stdio subprocess. For a
hosted deployment, run one mcp-server with `MCP_HTTP_ADDR` and
`MCP_AUTH_TOKEN` set and have every client point at the shared URL:

```bash
claude mcp add --transport http a1-knowledge-graph https://<host>/mcp \
  --header "Authorization: Bearer ${MCP_AUTH_TOKEN}"
```

`/mcp` is the protocol endpoint; `/health` is an unauthenticated liveness
probe for load balancers. Two constraints to plan around:

- MCP clients require HTTPS for non-loopback URLs. This repo ships the
  container only (a plain-HTTP listener, same as query-service); TLS
  termination in front of it - ALB, certificate, DNS - is deployment
  infrastructure outside this repo, and must exist before remote clients
  can connect.
- `MCP_AUTH_TOKEN` ends up in every engineer's Claude Code config, so treat
  it as the widest-distributed secret in the system. It is deliberately a
  different token from `QUERY_AUTH_TOKEN` (which only travels between
  mcp-server/web and query-service inside the deployment) so the two rotate
  independently.

## Webhook-triggered indexing

Interval mode alone re-checks every repo on a fixed clock, so worst-case
freshness is one full interval. Webhook mode flips that: GitHub tells the
indexer the moment a push lands, and only the pushed repos re-index.

```bash
GITHUB_WEBHOOK_SECRET=<secret> go run ./cmd/indexer --all \
  --webhook-addr 0.0.0.0:8091 --interval 30m
```

- `POST /webhook/github` - the delivery endpoint. Every request must carry a
  valid `X-Hub-Signature-256` HMAC (hence the required
  `GITHUB_WEBHOOK_SECRET`); anything unsigned or mis-signed gets a 401
  without touching the payload. `ping` is answered, `push` events for a
  configured repo+branch queue a re-index, everything else is acknowledged
  and ignored - so an org-wide webhook that also covers unconfigured repos
  is fine.
- Deliveries **coalesce**: the handler returns 202 immediately and the
  daemon starts one cycle ~10s later covering everything queued in that
  window, so a release train pushing ten repos costs one cycle, not ten.
  Duplicate and out-of-order deliveries are harmless (indexing always syncs
  to HEAD).
- `--interval` becomes the **reconciliation sweep** and stays mandatory.
  GitHub webhook delivery is at-most-once - a delivery that fails while the
  indexer is down is never retried - so the sweep is what bounds staleness:
  every `--interval` it fetches all repos and re-indexes any whose HEAD
  moved without a delivery arriving. Webhooks are the fast path; the sweep
  is the guarantee. 30m keeps even the missed-delivery worst case
  comfortably inside a 1-hour freshness target.
- `GET /status` (bearer `INDEXER_STATUS_TOKEN`) - per-repo
  `last_indexed_commit`, timestamps, failure counts, and whether a delivery
  is queued but not yet processed. A repo showing `last_status: success`,
  `pending_reindex: false`, and a `last_attempt_at` within one sweep is
  fully caught up. `GET /health` is an unauthenticated liveness probe.
- Targeted webhook cycles never run retirement reconciliation - only full
  sweeps do - so a delivery can never trigger data deletion.

GitHub-side setup (repo or org webhook, or the GitHub App's webhook): URL
`https://<host>/webhook/github`, content type `application/json`, the same
secret, and only the **Push** event. Like the other HTTP services, the
listener itself is plain HTTP - GitHub requires HTTPS, so it sits behind the
same TLS front (ALB + certificate) as everything else.

## Runbook

### Automatic repository discovery (GitHub App)

With GitHub App auth configured (the `GITHUB_APP_*` env vars), the manifest
can come from the App installation itself instead of a hand-maintained list:

```yaml
discovery:
  enabled: true
  # ttl: 10m          # max GitHub API refresh frequency (default 10m)
repositories: []      # optional static extras / overrides, see below
```

The set of repos the App is installed on IS the manifest: **installing the
App on a repo adds it to the graph, uninstalling removes it** (normal
retirement flow: warning, 1h grace, then deletion). Both actions happen in
the GitHub UI with GitHub's own audit trail - no config edit, no redeploy.
Changes take effect within one `ttl` + one cycle.

Details:
- Archived repos and names failing the indexer's validation are skipped
  (logged). Each repo indexes its default branch.
- Static `repositories:` entries still work and win by name - keep one to
  pin a non-default branch, or to index something the App can't see.
- If the GitHub API is unreachable, the last successful listing is served
  (an outage can never shrink the manifest and trigger retirements); a
  failure before the first successful listing fails the cycle instead.
- The webhook endpoint and `/status` resolve against the live manifest, so
  newly installed repos match deliveries without a restart.

### Adding a repository

With discovery enabled: install the GitHub App on the repo - done.

With a static manifest:

1. Add an entry to `config/repos.yaml`:
   ```yaml
   - name: payments-service        # unique; filesystem+graph identifier
     url: git@github.com:xyz/payments-service.git
     branch: main
   ```
2. `go run ./cmd/indexer --repo payments-service` to index it immediately, or
   wait for the next continuous cycle. No other repos are re-indexed.

Git auth is whatever the local `git` is configured for (SSH keys, credential
helper). The indexer disables interactive prompts, so a missing credential
fails fast instead of hanging.

To generate a one-off static manifest for a whole GitHub org (every
non-archived repo pushed in the last 90 days):

```bash
GITHUB_TOKEN=<read-only PAT> go run ./cmd/repogen --org your-org > config/repos.yaml
```

Review the output before committing; `--days` and `--ssh` adjust the window
and URL style. (Where PATs aren't allowed, prefer `discovery.enabled` above.)

### Removing a repository

With discovery enabled: uninstall the GitHub App from the repo (or remove it
from the installation's repository list); retirement below applies the same
way.

With a static manifest: delete its entry from `config/repos.yaml`. The next full indexing run warns
that the repo is in the graph but not in the config; a later run (at least an
hour after the warning) deletes its graph data - owned entities, stamped
edges, the repository node, and any shared nodes nothing else references.
Re-adding the entry before that happens cancels the retirement; re-adding it
after re-indexes from scratch. Targeted runs (`--repo`) never retire
anything.

If more than half the repos in the graph are missing from the config at once,
retirement is skipped entirely and an error is logged instead - that pattern
almost always means the indexer was pointed at the wrong config file, not a
real retirement. To genuinely retire that many repos, remove them in batches
of less than half the graph, one grace period apart.

### Rotating the auth token

Set the new `QUERY_AUTH_TOKEN` on query-service and restart, then update
mcp-server / clients. `MCP_AUTH_TOKEN` rotates separately: set the new value
on the hosted mcp-server and have engineers update their Claude Code config -
a `QUERY_AUTH_TOKEN` rotation doesn't touch it, and vice versa. Rotate
`MCP_AUTH_TOKEN` first on any suspicion of a leak; it's the token with the
widest distribution.

### Forcing a re-index

`go run ./cmd/indexer --repo <name> --force` re-runs the pipeline even when
HEAD hasn't moved (e.g. after an extractor fix or a graph wipe).

### Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `another indexer is already running against workdir` | a second indexer holds the flock; stop it. (Windows: locking is a no-op, don't run two.) |
| `clone target ... is non-empty and not a git repo` | interrupted clone; delete `workdir/repos/<name>` and re-run |
| `remote mismatch at ...` | repo URL changed in config; delete the clone to re-clone from the new URL |
| `state file ... was malformed; quarantined` | state was recovered automatically; affected repos re-index on next cycle |
| indexer summary shows `[MISMATCH: N in graph]` | node_key collisions: imported count ≠ Neo4j count; investigate before trusting results |
| stale results after extractor changes | re-run with `--force`; old shared nodes are reaped only when orphaned |

### Web UI authentication

The web UI never embeds the bearer token - a `VITE_*` variable is baked into
the shipped JS bundle, readable by anyone who can load the page. Instead the
proxy in front of the UI injects `Authorization` server-side: in dev that's
the Vite proxy (export `QUERY_AUTH_TOKEN` before `npm run dev`); for a shared
deployment use any reverse proxy that forwards `/api` with the header. As a
fallback for a cross-origin API, set `QUERY_CORS_ORIGIN` on query-service.

### Operational invariants

- **One indexer per workdir.** Enforced by flock on unix; convention on Windows.
- **One indexer per Neo4j database.** Enforced in the database itself: each
  indexer (and `cmd/importer`) acquires a writer lease (`IndexerLease` node)
  on startup and a background heartbeat renews it every `ttl/4`. A second
  writer either refuses to start while the lease is held, or - if it started
  because the lease had genuinely expired - gets forced to stop the moment
  its own renewal is refused. `--steal-lease` (indexer only) is the operator
  recovery path for a stuck lease left by a crash; use it deliberately.
- `workdir/` is disposable *except* `state.json` (and even that self-heals -
  deleting it just forces a full re-index).
- The importer is idempotent: re-importing the same commit is a no-op upsert.

## Tests

```
go test ./...
```

Extractors are covered by table tests over fixture files (`internal/extract/*/
*_test.go`); the import pipeline is tested against a fake Neo4j client
(`internal/importer/importer_test.go`). No test needs a live database.

## Known limitations

- Extractors are heuristic (regex-level). Edges they emit carry
  `confidence: INFERRED` - treat them as leads, not proofs.
- Windows hosts don't get workdir locking.
