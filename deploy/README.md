# Deployment (test / pilot)

One `docker compose` stack: Neo4j, the query API, a continuous indexer, and a
Caddy proxy that serves the web UI and injects the bearer token server-side
(the token never reaches the browser).

```
[browser] ──▶ web (Caddy :80)
               ├─ /        static UI bundle
               └─ /api/*   ──▶ query-service:8080  (+ Authorization header)
[indexer] ──▶ GitHub (read-only creds) ──▶ graphify ──▶ neo4j
```

## Bring it up

```bash
cp .env.example .env      # fill in NEO4J_PASSWORD, QUERY_AUTH_TOKEN, GITHUB_TOKEN
# put real repo URLs in config/repos.yaml (see cmd/repogen to generate it)
docker compose up -d --build
```

The portal is at `http://<host>/` (or `:$WEB_PORT`). Watch the first index
cycle with `docker compose logs -f indexer`.

## Git auth for private repos

Pick one:

- **HTTPS + token** (default): set `GITHUB_TOKEN` in `.env` to a read-only
  fine-grained PAT (Contents: read) or GitHub App installation token, and use
  `https://github.com/<org>/<repo>.git` URLs in `repos.yaml`.
- **SSH**: uncomment the `~/.ssh` mount on the indexer service and use
  `git@github.com:` URLs.

## MCP access for engineers

Each engineer runs `mcp-server` locally (stdio, spawned by Claude Code) with:

```
QUERY_SERVICE_URL=http://<host>:8080   # requires publishing 8080 in compose
QUERY_AUTH_TOKEN=<the shared token>
```

Publishing 8080 is commented out in `docker-compose.yaml`; enable it when the
pilot team needs MCP. Web-portal users never need the token.

## Operational notes

- **One indexer per Neo4j database** — never run a second indexer container
  (or a host-side indexer) against the same DB.
- `indexer-workdir` volume holds clones + `state.json`; deleting it just
  forces a full re-index.
- `INDEX_INTERVAL` defaults to 1h. Cycles skip unchanged repos after a cheap
  fetch, so a short interval costs little and buys retry margin against the
  24h freshness SLA.
- graphify runs in `update` mode: AST-only, no LLM API calls — no code or
  credentials leave the box except git fetches to GitHub.
- Neo4j ports are published on loopback only, for operator debugging.

## Path to AWS

Same shape, managed pieces: containers → ECS services, Caddy's job → ALB
(terminate TLS + SSO there), `.env` → Secrets Manager, volumes → EBS/EFS.
Nothing in this stack is throwaway.
