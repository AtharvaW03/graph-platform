package admin

import (
	"net/http"
	"strings"

	"a1-knowledge-graph/internal/keys"
	"a1-knowledge-graph/internal/usage"
)

// Drill-down endpoints. The list screens answer "what is there"; these
// answer "tell me about this one", which is the context an operator needs
// before acting - who relies on a repository before you touch it, what
// someone actually uses before you revoke their access.

type repoDetail struct {
	Name       string        `json:"name"`
	Nodes      int           `json:"nodes"`
	AgeSeconds int64         `json:"age_seconds"`
	Stale      bool          `json:"stale"`
	Indexed    bool          `json:"indexed"`
	Days       int           `json:"days"`
	Requests   int           `json:"requests"`
	Queriers   []usage.Count `json:"queriers"`
}

// repoDetailHandler serves GET /admin/api/repos/{name}.
func (h *Handler) repoDetailHandler(w http.ResponseWriter, r *http.Request, _ string) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repository name required"})
		return
	}
	ctx := r.Context()
	days := intParam(r, "days", 30)
	out := repoDetail{Name: name, Days: days}

	if h.deps.Graph != nil {
		repos, err := h.deps.Graph.ListRepositories(ctx)
		if err != nil {
			serverError(w, "list repositories", err)
			return
		}
		for _, rp := range repos {
			if rp.Name == name {
				out.Nodes = rp.Nodes
				out.Indexed = true
				break
			}
		}
		fresh, err := h.deps.Graph.Freshness(ctx)
		if err != nil {
			serverError(w, "freshness", err)
			return
		}
		for _, f := range fresh {
			if f.Repo == name {
				out.AgeSeconds = f.AgeSeconds
				out.Stale = f.Stale
				break
			}
		}
	}
	if h.deps.Usage != nil {
		var err error
		if out.Requests, err = h.deps.Usage.RepoTotal(ctx, name, days); err != nil {
			serverError(w, "repo total", err)
			return
		}
		if out.Queriers, err = h.deps.Usage.RepoActivity(ctx, name, days); err != nil {
			serverError(w, "repo activity", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type actorDetail struct {
	Actor    string        `json:"actor"`
	Days     int           `json:"days"`
	Requests int           `json:"requests"`
	Keys     []keys.Key    `json:"keys"`
	Repos    []usage.Count `json:"repos"`
}

// actorDetailHandler serves GET /admin/api/actors/{actor}. Deliberately
// limited to what a revoke decision needs: which keys the identity holds,
// and which repositories they have actually used. No scoring, no ranking
// against other people - this exists to inform an access decision, not to
// profile anyone.
func (h *Handler) actorDetailHandler(w http.ResponseWriter, r *http.Request, _ string) {
	actor := strings.TrimSpace(r.PathValue("actor"))
	if actor == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "actor required"})
		return
	}
	ctx := r.Context()
	days := intParam(r, "days", 30)
	out := actorDetail{Actor: actor, Days: days, Keys: []keys.Key{}, Repos: []usage.Count{}}

	if h.deps.Keys != nil {
		all, err := h.deps.Keys.List(ctx)
		if err != nil {
			serverError(w, "list keys", err)
			return
		}
		for _, k := range all {
			if strings.EqualFold(k.Owner, actor) {
				out.Keys = append(out.Keys, k)
			}
		}
	}
	if h.deps.Usage != nil {
		var err error
		if out.Requests, err = h.deps.Usage.ActorTotal(ctx, actor, days); err != nil {
			serverError(w, "actor total", err)
			return
		}
		repos, err := h.deps.Usage.ActorActivity(ctx, actor, days)
		if err != nil {
			serverError(w, "actor activity", err)
			return
		}
		if repos != nil {
			out.Repos = repos
		}
	}
	writeJSON(w, http.StatusOK, out)
}
