#!/bin/sh
# Entrypoint for the indexer container: wires up git credentials from the
# environment, then runs the indexer in continuous mode.
#
# Auth options (pick one):
#   - GITHUB_TOKEN: a read-only fine-grained PAT / GitHub App installation
#     token. Stored in an in-container credential store (never in the image).
#   - Mount SSH keys to /root/.ssh (read-only) and use git@ URLs in repos.yaml.
set -eu

if [ -n "${GITHUB_TOKEN:-}" ]; then
  git config --global credential.helper store
  # x-access-token works for both PATs and GitHub App installation tokens.
  printf 'https://x-access-token:%s@github.com\n' "$GITHUB_TOKEN" > ~/.git-credentials
  chmod 600 ~/.git-credentials
fi

exec indexer \
  --all \
  --config /app/config/repos.yaml \
  --workdir /workdir \
  --interval "${INDEX_INTERVAL:-1h}" \
  "$@"
