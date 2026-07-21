# A1 Knowledge Graph

Clones the repositories you list, indexes them into a Neo4j graph, and lets you
ask questions about the code across repositories and languages.

The documents in `docs/graphify/` are about graphify, the tool used for AST
extraction. They describe that tool, not this project.

## Quickstart

You will need Go 1.26+, Neo4j 5.x, git, and the graphify CLI on your PATH.

```powershell
# Install uv (graphify is distributed as a Python package)
curl -LsSf https://astral.sh/uv/install.sh | sh   # macOS
winget install astral-sh.uv                       # Windows

# Graphify: the official PyPI package is `graphifyy` (double-y)
uv tool install graphifyy

# Neo4j
docker run -d --name neo4j-knowledge-graph -p 7474:7474 -p 7687:7687 -e NEO4J_AUTH=neo4j/<password> neo4j:5

# Environment
$env:NEO4J_PASSWORD = "<password>"
$env:QUERY_AUTH_TOKEN = "<token>"   # optional for local dev

# Index the repos in config/repos.yaml
go run ./cmd/indexer --all                 # one-shot
go run ./cmd/indexer --all --interval 15m  # continuous

# Serve queries (listens on :8080)
go run ./cmd/query-service
```

For the web UI, run `cd web && npm install && npm run dev`
(http://localhost:5173). Set `QUERY_AUTH_TOKEN` before you start the dev server.

## Indexer flags

| Flag | Default | Purpose |
|---|---|---|
| `--config` | `config/repos.yaml` | path to the manifest to index |
| `--workdir` | `workdir` | directory for clones, graphify outputs, and state |
| `--repo` | *(empty = all)* | comma-separated repository names to index; mutually exclusive with `--all` |
| `--all` | `false` | explicit all-repositories mode |
| `--force` | `false` | re-index even if HEAD matches the previously-indexed commit |
| `--interval` | `0` (one-shot) | if set, run continuously, indexing every interval (e.g. `15m`) |
| `--webhook-addr` | *(empty)* | serve a GitHub push-webhook endpoint (e.g. `0.0.0.0:8091`); requires `--interval` and `GITHUB_WEBHOOK_SECRET` |
| `--lease-ttl` | `15m` | writer lease duration; a crashed process's lease self-expires after this |
| `--steal-lease` | `false` | take the writer lease unconditionally; recovers a stuck lease |

Repository names are only ever read from `--repo` or the config file — a bare
name on the command line (`go run ./cmd/indexer my-repo`) is not a filter and
is rejected rather than silently indexing everything.

## Environment variables

| Variable | Used by | Default | Purpose |
|---|---|---|---|
| `NEO4J_PASSWORD` | indexer, query-service, importer | *(required)* | Neo4j auth |
| `NEO4J_URI` | indexer, query-service | `neo4j://127.0.0.1:7687` | Neo4j endpoint |
| `NEO4J_USER` | indexer, query-service | `neo4j` | Neo4j user |
| `QUERY_PORT` | query-service | `8080` | listen port |
| `QUERY_BIND` | query-service | `127.0.0.1` (all interfaces when a token is set) | listen address |
| `QUERY_AUTH_TOKEN` | query-service, mcp-server | *(empty = no auth)* | bearer token query-service requires on every route except `/health` and `/ready`; mcp-server presents it to query-service |
| `QUERY_SERVICE_URL` | mcp-server | `http://localhost:8080` | query-service base URL |
| `MCP_HTTP_ADDR` | mcp-server | *(empty = stdio)* | serve MCP over HTTP on this address (e.g. `0.0.0.0:8090`) instead of stdio |
| `MCP_AUTH_TOKEN` | mcp-server | *(required in HTTP mode on a reachable address)* | bearer token for incoming MCP connections; separate from `QUERY_AUTH_TOKEN` |
| `MCP_ALLOW_NO_AUTH` | mcp-server | *(empty)* | set to `1` to allow HTTP mode with no token on a reachable address; otherwise it fails closed |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY_PATH` | indexer | *(empty = use `GIT_TOKEN`)* | GitHub App auth for cloning private repos; all three required together |
| `GIT_TOKEN` | indexer | *(empty = public repos only)* | PAT fallback for HTTPS clones; ignored when the `GITHUB_APP_*` vars are set |
| `GITHUB_WEBHOOK_SECRET` | indexer | *(required with `--webhook-addr`)* | HMAC secret GitHub signs webhook deliveries with |

## Common tasks

To add a repository, put an entry in `config/repos.yaml` with a name, url, and
branch, then index it:

```yaml
- name: payments-service
  url: git@github.com:xyz/payments-service.git
  branch: main
```

The url can also point at a git repository already on your machine, using a
`file://` path with the absolute location:

```yaml
- name: my-local-service
  url: file:///home/me/code/my-local-service   # Windows: file:///C:/code/my-local-service
  branch: main
```

The folder has to be a git repo and the branch has to exist.

To index one repo:

```bash
go run ./cmd/indexer --repo payments-service
```

To re-index a single repo after changing an extractor, add `--force`:

```bash
go run ./cmd/indexer --repo <name> --force
```

To rebuild the whole graph from scratch, for example after wiping Neo4j or
updating graphify, combine the two:

```bash
go run ./cmd/indexer --all --force
```

## MCP server

`mcp-server` hands the graph to MCP-aware tools such as the Claude Code CLI.
Start query-service first, then build it:

```bash
go build -o mcp-server ./cmd/mcp-server
```

**Local (stdio).** Claude Code spawns the binary as a subprocess:

```bash
claude mcp add a1-knowledge-graph \
  -e QUERY_SERVICE_URL=http://localhost:8080 \
  -e QUERY_AUTH_TOKEN=<token> \
  -- /absolute/path/to/mcp-server
```

**Hosted (HTTP).** For a shared, deployed server, run mcp-server with
`MCP_HTTP_ADDR` and `MCP_AUTH_TOKEN` set, and point every client at the URL:

```bash
claude mcp add --transport http a1-knowledge-graph https://<host>/mcp \
  --header "Authorization: Bearer <MCP_AUTH_TOKEN>"
```

MCP clients require HTTPS for non-loopback URLs, so the hosted server sits
behind a TLS front. On a reachable address mcp-server refuses to start without
`MCP_AUTH_TOKEN` unless `MCP_ALLOW_NO_AUTH=1` is set.

## Deployment

This README covers local use. For the deployed setup — GitHub App auth,
webhook-triggered indexing with a reconciliation sweep, repository discovery,
data-freshness reporting, and the container images — see `AGENTS.md`
(architecture and operating invariants) and `deploy/README.md` (images and
runtime).
