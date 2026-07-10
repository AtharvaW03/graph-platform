# Container images

Three images. `query-service` and `indexer` build from the repo root (they need
`go.mod`/`internal` in their build context); `web` builds from `web/`.

## query-service

```
docker build -f deploy/Dockerfile.query-service -t graph-platform/query-service .
docker run --rm -p 8080:8080 \
  -e NEO4J_PASSWORD=... -e NEO4J_URI=neo4j://host.docker.internal:7687 \
  -e QUERY_BIND=0.0.0.0 -e QUERY_AUTH_TOKEN=... \
  graph-platform/query-service
```

Distroless final stage, no shell. `QUERY_BIND` must be set (or `QUERY_AUTH_TOKEN`
set, which flips the default bind to all interfaces) or the service binds to
127.0.0.1 and port publishing does nothing - see cmd/query-service/main.go.

## indexer

```
docker build -f deploy/Dockerfile.indexer -t graph-platform/indexer .
docker run --rm \
  -e NEO4J_PASSWORD=... -e NEO4J_URI=neo4j://host.docker.internal:7687 \
  -e GIT_TOKEN=... \
  -v indexer-workdir:/app/workdir \
  -v "$(pwd)/config/repos.yaml:/app/config/repos.yaml:ro" \
  graph-platform/indexer /usr/local/bin/indexer --config config/repos.yaml --all
```

Final stage is `python:3.12-slim` with `git` and `graphifyy==0.9.9` (the PyPI
package; the CLI command is `graphify`). `deploy/indexer-entrypoint.sh` sets up
git credentials from `GIT_TOKEN` when present, then `exec`s the container
command - so the binary + its flags are the docker `command`, not baked into
`ENTRYPOINT`.

## web

```
docker build -f web/Dockerfile -t graph-platform/web web
docker run --rm -p 8081:80 \
  -e QUERY_SERVICE_URL=http://query-service:8080 \
  -e QUERY_AUTH_TOKEN=... \
  graph-platform/web
```

`nginx:alpine` serving the Vite build with SPA fallback, proxying `/api/*` to
query-service and stripping the `/api` prefix (matches `web/vite.config.ts`'s
dev proxy). `QUERY_AUTH_TOKEN` is read at container start via nginx's
envsubst-templates mechanism (`/etc/nginx/templates/default.conf.template`)
and injected into the proxied `Authorization` header - it never reaches the
browser bundle, build-time or otherwise.

## Full stack

See `../docker-compose.yml` and `../.env.example` at the repo root.
