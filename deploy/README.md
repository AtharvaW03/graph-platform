# Container images

Four images. `query-service`, `mcp-server`, and `indexer` build from the repo
root (they need `go.mod`/`internal` in their build context); `web` builds from
`web/`.

## query-service

```
docker build -f deploy/Dockerfile.query-service -t a1-knowledge-graph/query-service .
docker run --rm -p 8080:8080 \
  -e NEO4J_PASSWORD=... -e NEO4J_URI=neo4j://host.docker.internal:7687 \
  -e QUERY_BIND=0.0.0.0 -e QUERY_AUTH_TOKEN=... \
  a1-knowledge-graph/query-service
```

Distroless final stage, no shell. `QUERY_BIND` must be set (or `QUERY_AUTH_TOKEN`
set, which flips the default bind to all interfaces) or the service binds to
127.0.0.1 and port publishing does nothing - see cmd/query-service/main.go.

## mcp-server

```
docker build -f deploy/Dockerfile.mcp-server -t a1-knowledge-graph/mcp-server .
docker run --rm -p 8090:8090 \
  -e MCP_HTTP_ADDR=0.0.0.0:8090 -e MCP_AUTH_TOKEN=... \
  -e QUERY_SERVICE_URL=http://query-service:8080 -e QUERY_AUTH_TOKEN=... \
  a1-knowledge-graph/mcp-server
```

Distroless final stage, no shell. This image only makes sense with
`MCP_HTTP_ADDR` set: without it the binary serves MCP over stdio, and a
detached container has nothing attached to stdio - it just blocks silently.
`MCP_AUTH_TOKEN` gates incoming MCP connections (`/health` stays open for LB
probes); `QUERY_AUTH_TOKEN` is the *outbound* credential this process uses
against query-service - two different boundaries, rotate them independently.
On a reachable (non-loopback) address the server refuses to start without
`MCP_AUTH_TOKEN` unless `MCP_ALLOW_NO_AUTH=1` is set, so a missing secret
fails closed rather than exposing the graph. Clients must reach this through
an HTTPS front (ALB + certificate) - MCP clients refuse plain HTTP on
non-loopback URLs.

## indexer

```
docker build -f deploy/Dockerfile.indexer -t a1-knowledge-graph/indexer .
docker run --rm \
  -e NEO4J_PASSWORD=... -e NEO4J_URI=neo4j://host.docker.internal:7687 \
  -e GIT_TOKEN=... \
  -v indexer-workdir:/app/workdir \
  -v "$(pwd)/config/repos.yaml:/app/config/repos.yaml:ro" \
  a1-knowledge-graph/indexer /usr/local/bin/indexer --config config/repos.yaml --all
```

Final stage is `python:3.12-slim` with `git` and `graphifyy==0.9.23` (the PyPI
package; the CLI command is `graphify`). `deploy/indexer-entrypoint.sh` sets up
git credentials from `GIT_TOKEN` when present, then `exec`s the container
command - so the binary + its flags are the docker `command`, not baked into
`ENTRYPOINT`.

For webhook-driven indexing, add `--webhook-addr 0.0.0.0:8091 --interval 30m`
to the command, set `GITHUB_WEBHOOK_SECRET` (required - deliveries are
HMAC-verified), and publish the port. `/webhook/github` must be reachable by
GitHub over HTTPS, so it needs the same TLS front as the other services;
`/health` is the LB probe, `/status` (bearer `INDEXER_STATUS_TOKEN`) reports
per-repo indexing state. The indexer stays a singleton either way - never run
two replicas against one database.

## web

```
docker build -f web/Dockerfile -t a1-knowledge-graph/web web
docker run --rm -p 8081:80 \
  -e QUERY_SERVICE_URL=http://query-service:8080 \
  -e QUERY_AUTH_TOKEN=... \
  a1-knowledge-graph/web
```

`nginx:alpine` serving the Vite build with SPA fallback, proxying `/api/*` to
query-service and stripping the `/api` prefix (matches `web/vite.config.ts`'s
dev proxy). `QUERY_AUTH_TOKEN` is read at container start via nginx's
envsubst-templates mechanism (`/etc/nginx/templates/default.conf.template`)
and injected into the proxied `Authorization` header - it never reaches the
browser bundle, build-time or otherwise.

## Full stack

See `../docker-compose.yml` and `../.env.example` at the repo root.
