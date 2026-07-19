package index

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// maxWebhookBody caps how much of a delivery we read. GitHub's own payload
// limit is 25 MB; anything larger is not a real GitHub delivery.
const maxWebhookBody = 25 << 20

// PendingSet is the coalescing hand-off between webhook deliveries and the
// indexing loop. Deliveries Add repo names from HTTP handler goroutines; the
// indexing loop Drains them at the start of a cycle. A set, not a queue:
// multiple pushes to one repo before the next cycle coalesce into one
// re-index (indexing always syncs to HEAD).
type PendingSet struct {
	mu    sync.Mutex
	names map[string]struct{}
	ch    chan struct{}
}

func NewPendingSet() *PendingSet {
	return &PendingSet{names: map[string]struct{}{}, ch: make(chan struct{}, 1)}
}

// Add marks a repository as needing a re-index and signals C. Safe for
// concurrent use.
func (p *PendingSet) Add(name string) {
	p.mu.Lock()
	p.names[name] = struct{}{}
	p.mu.Unlock()
	select {
	case p.ch <- struct{}{}:
	default:
	}
}

// Drain returns the pending names sorted and clears the set, consuming any
// buffered signal so a later Wait doesn't wake for names already drained.
// Returns nil when nothing is pending. Single-consumer by design (the
// indexing loop); Add may safely race it.
func (p *PendingSet) Drain() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.ch:
	default:
	}
	if len(p.names) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.names))
	for n := range p.names {
		out = append(out, n)
	}
	clear(p.names)
	sort.Strings(out)
	return out
}

// Snapshot returns the pending names sorted without draining, for status
// reporting.
func (p *PendingSet) Snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.names) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.names))
	for n := range p.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// C signals that at least one name may be pending. Buffered(1) and coalesced:
// many Adds between reads collapse into one wake-up.
func (p *PendingSet) C() <-chan struct{} { return p.ch }

// GitHubWebhookHandler accepts GitHub push-event deliveries and enqueues
// the matching configured repositories for re-indexing. It does no indexing
// work itself: GitHub abandons slow deliveries and never retries failures,
// so the handler verifies, enqueues, and returns 202.
//
// Every delivery must carry a valid X-Hub-Signature-256 HMAC; the secret is
// required at construction.
type GitHubWebhookHandler struct {
	secret  []byte
	pending *PendingSet
	log     *log.Logger
	// source serves the current manifest per delivery rather than a map
	// frozen at startup, so discovery-driven changes (App installed on a new
	// repo) are matchable without a restart. ConfigJobSource serves a static
	// list; DiscoveryJobSource serves its TTL-cached listing - either way
	// this is an in-memory read, not a GitHub API call per delivery.
	source JobSource
}

func NewGitHubWebhookHandler(source JobSource, secret string, pending *PendingSet, logger *log.Logger) (*GitHubWebhookHandler, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("webhook secret must not be empty")
	}
	if pending == nil {
		return nil, fmt.Errorf("pending set is required")
	}
	if source == nil {
		return nil, fmt.Errorf("job source is required")
	}
	return &GitHubWebhookHandler{
		secret:  []byte(secret),
		pending: pending,
		log:     logger,
		source:  source,
	}, nil
}

func (h *GitHubWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")

	// Verify the signature before anything else; unsigned payloads never
	// reach the JSON parser.
	if !validSignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		h.log.Printf("webhook: rejected delivery %q: bad or missing signature", delivery)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "pong")
	case "push":
		h.handlePush(w, r.Context(), body, delivery)
	default:
		// 2xx so GitHub doesn't mark the delivery failed; subscribing to
		// extra events is a GitHub-side config choice, not an error here.
		h.log.Printf("webhook: delivery %q: event %q ignored", delivery, event)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event %q ignored", event)
	}
}

// pushEvent is the minimal slice of GitHub's push payload we act on.
type pushEvent struct {
	Ref        string `json:"ref"`
	Deleted    bool   `json:"deleted"`
	After      string `json:"after"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

func (h *GitHubWebhookHandler) handlePush(w http.ResponseWriter, ctx context.Context, body []byte, delivery string) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed push payload", http.StatusBadRequest)
		return
	}
	if ev.Deleted {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "branch deletion ignored")
		return
	}

	repos, err := h.source.Repositories(ctx)
	if err != nil {
		// Can only happen while discovery has never succeeded; 5xx is honest
		// (GitHub shows the delivery as failed) and the reconciliation sweep
		// still covers the repo once discovery recovers.
		h.log.Printf("webhook: delivery %q: manifest unavailable: %v", delivery, err)
		http.Error(w, "repository manifest unavailable", http.StatusServiceUnavailable)
		return
	}
	byURL := make(map[string][]Repository, len(repos))
	for _, r := range repos {
		key := normalizeRemoteURL(r.URL)
		byURL[key] = append(byURL[key], r)
	}

	var matched []Repository
	for _, candidate := range []string{ev.Repository.CloneURL, ev.Repository.SSHURL, ev.Repository.HTMLURL} {
		if candidate == "" {
			continue
		}
		if repos, ok := byURL[normalizeRemoteURL(candidate)]; ok {
			matched = repos
			break
		}
	}
	if len(matched) == 0 {
		h.log.Printf("webhook: delivery %q: repository %q not in the indexer config, ignored", delivery, ev.Repository.FullName)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "repository not configured, ignored")
		return
	}

	var queued []string
	for _, repo := range matched {
		if ev.Ref != "refs/heads/"+repo.Branch {
			continue
		}
		h.pending.Add(repo.Name)
		queued = append(queued, repo.Name)
	}
	if len(queued) == 0 {
		h.log.Printf("webhook: delivery %q: %s push to %q ignored (tracking a different branch)", delivery, ev.Repository.FullName, ev.Ref)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ref not tracked, ignored")
		return
	}

	h.log.Printf("webhook: delivery %q: queued re-index of %s (%s @ %s)", delivery, strings.Join(queued, ", "), ev.Ref, shortSHA(ev.After))
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, "queued: %s", strings.Join(queued, ", "))
}

// validSignature checks GitHub's X-Hub-Signature-256 header ("sha256=<hex>")
// against the HMAC of the raw body. hmac.Equal is constant-time.
func validSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}

// normalizeRemoteURL reduces the many spellings of a git remote to a
// comparable "host/path" key: scheme, user@, trailing slash, .git suffix, and
// case are all stripped, and scp-like syntax (git@host:org/repo) is folded
// into the same shape. This is a matching key, never something we fetch.
func normalizeRemoteURL(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if at := strings.Index(s, "@"); at >= 0 && strings.Contains(s[at:], ":") {
		// scp-like: git@github.com:org/repo
		s = strings.Replace(s[at+1:], ":", "/", 1)
	}
	// user@ inside a URL form (https://user@host/...): strip up to the last @
	// before the first slash.
	if slash := strings.IndexByte(s, '/'); slash >= 0 {
		if at := strings.LastIndex(s[:slash], "@"); at >= 0 {
			s = s[at+1:]
		}
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimSuffix(s, "/")
}
