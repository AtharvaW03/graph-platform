#!/bin/sh
set -eu

# Non-interactive private-HTTPS clones: if GIT_TOKEN is set, configure a
# credential store entry so `git clone`/`fetch` in the syncer never prompts.
# Public repos work with no token at all.
if [ -n "${GIT_TOKEN:-}" ]; then
	git config --global credential.helper store
	touch "$HOME/.git-credentials"
	chmod 600 "$HOME/.git-credentials"
	echo "https://x-access-token:${GIT_TOKEN}@github.com" > "$HOME/.git-credentials"
	export GIT_TERMINAL_PROMPT=0
fi

exec "$@"
