package index

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "test-webhook-secret"

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestHandler(t *testing.T, repos []Repository) (*GitHubWebhookHandler, *PendingSet) {
	t.Helper()
	pending := NewPendingSet()
	h, err := NewGitHubWebhookHandler(fakeSource{repos: repos}, testSecret, pending, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewGitHubWebhookHandler: %v", err)
	}
	return h, pending
}

// postWebhook sends body to the handler with the given event name and
// signature header (empty sigHeader means "omit the header").
func postWebhook(h http.Handler, event, sigHeader string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery-1")
	if sigHeader != "" {
		req.Header.Set("X-Hub-Signature-256", sigHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func pushBody(t *testing.T, cloneURL, ref string) []byte {
	t.Helper()
	payload := map[string]any{
		"ref":     ref,
		"after":   "0123456789abcdef0123456789abcdef01234567",
		"deleted": false,
		"repository": map[string]any{
			"full_name": "org/some-repo",
			"clone_url": cloneURL,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWebhookHandler_RequiresSecretAtConstruction(t *testing.T) {
	_, err := NewGitHubWebhookHandler(fakeSource{repos: threeRepos()}, "  ", NewPendingSet(), log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected an error for an empty secret")
	}
}

func TestWebhookHandler_RejectsBadOrMissingSignature(t *testing.T) {
	h, pending := newTestHandler(t, []Repository{{Name: "svc", URL: "https://github.com/org/svc.git", Branch: "main"}})
	body := pushBody(t, "https://github.com/org/svc.git", "refs/heads/main")

	for name, sig := range map[string]string{
		"missing":      "",
		"wrong secret": sign("not-the-secret", body),
		"not hex":      "sha256=zzzz",
		"wrong scheme": "sha1=" + strings.Repeat("ab", 20),
	} {
		rec := postWebhook(h, "push", sig, body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s signature: got %d, want 401", name, rec.Code)
		}
	}
	if got := pending.Drain(); got != nil {
		t.Fatalf("rejected deliveries must queue nothing, got %v", got)
	}
}

func TestWebhookHandler_SignedPushQueuesMatchingRepo(t *testing.T) {
	// Config uses scp-style ssh with different casing; the payload arrives as
	// the https clone_url. Normalization must make them meet.
	h, pending := newTestHandler(t, []Repository{
		{Name: "payments-service", URL: "git@github.com:Org/Payments-Service.git", Branch: "main"},
	})
	body := pushBody(t, "https://github.com/org/payments-service.git", "refs/heads/main")

	rec := postWebhook(h, "push", sign(testSecret, body), body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d (%s), want 202", rec.Code, rec.Body.String())
	}
	if got := pending.Drain(); len(got) != 1 || got[0] != "payments-service" {
		t.Fatalf("pending = %v, want [payments-service]", got)
	}
}

func TestWebhookHandler_BranchFilter(t *testing.T) {
	// Two config entries share one URL but track different branches; a push
	// to develop must queue only the develop entry.
	h, pending := newTestHandler(t, []Repository{
		{Name: "svc-main", URL: "https://github.com/org/svc.git", Branch: "main"},
		{Name: "svc-develop", URL: "https://github.com/org/svc.git", Branch: "develop"},
	})

	body := pushBody(t, "https://github.com/org/svc.git", "refs/heads/develop")
	rec := postWebhook(h, "push", sign(testSecret, body), body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rec.Code)
	}
	if got := pending.Drain(); len(got) != 1 || got[0] != "svc-develop" {
		t.Fatalf("pending = %v, want [svc-develop]", got)
	}

	// A push to a branch nobody tracks is acknowledged but queues nothing.
	body = pushBody(t, "https://github.com/org/svc.git", "refs/heads/feature/x")
	rec = postWebhook(h, "push", sign(testSecret, body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("untracked branch: got %d, want 200", rec.Code)
	}
	if got := pending.Drain(); got != nil {
		t.Fatalf("untracked branch queued %v", got)
	}
}

func TestWebhookHandler_IgnoresDeletionsUnknownReposAndOtherEvents(t *testing.T) {
	h, pending := newTestHandler(t, []Repository{{Name: "svc", URL: "https://github.com/org/svc.git", Branch: "main"}})

	// Branch deletion.
	del, _ := json.Marshal(map[string]any{
		"ref": "refs/heads/main", "deleted": true,
		"repository": map[string]any{"clone_url": "https://github.com/org/svc.git"},
	})
	if rec := postWebhook(h, "push", sign(testSecret, del), del); rec.Code != http.StatusOK {
		t.Fatalf("deletion: got %d, want 200", rec.Code)
	}

	// Repo not in the config (org-wide webhook is a supported setup).
	unknown := pushBody(t, "https://github.com/org/other.git", "refs/heads/main")
	if rec := postWebhook(h, "push", sign(testSecret, unknown), unknown); rec.Code != http.StatusOK {
		t.Fatalf("unknown repo: got %d, want 200", rec.Code)
	}

	// Ping and an unsubscribed event type both get 2xx so GitHub doesn't
	// mark the delivery failed.
	if rec := postWebhook(h, "ping", sign(testSecret, []byte(`{}`)), []byte(`{}`)); rec.Code != http.StatusOK {
		t.Fatalf("ping: got %d, want 200", rec.Code)
	}
	if rec := postWebhook(h, "issues", sign(testSecret, []byte(`{}`)), []byte(`{}`)); rec.Code != http.StatusOK {
		t.Fatalf("other event: got %d, want 200", rec.Code)
	}

	if got := pending.Drain(); got != nil {
		t.Fatalf("nothing should have queued, got %v", got)
	}
}

func TestWebhookHandler_MalformedPayloadAndMethod(t *testing.T) {
	h, _ := newTestHandler(t, threeRepos())

	bad := []byte(`{not json`)
	if rec := postWebhook(h, "push", sign(testSecret, bad), bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed payload: got %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/webhook/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: got %d, want 405", rec.Code)
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Org/Repo.git":       "github.com/org/repo",
		"https://github.com/org/repo.git/":      "github.com/org/repo",
		"git@github.com:org/repo":               "github.com/org/repo",
		"git@github.com:org/repo.git":           "github.com/org/repo",
		"ssh://git@github.com/org/repo":         "github.com/org/repo",
		"https://user@github.com/org/repo.git":  "github.com/org/repo",
		"  https://github.com/org/repo  ":       "github.com/org/repo",
		"https://ghe.internal.example/org/repo": "ghe.internal.example/org/repo",
	} {
		if got := normalizeRemoteURL(in); got != want {
			t.Errorf("normalizeRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPendingSet_CoalescesAndDrains(t *testing.T) {
	p := NewPendingSet()
	if got := p.Drain(); got != nil {
		t.Fatalf("empty drain = %v, want nil", got)
	}
	p.Add("b")
	p.Add("a")
	p.Add("b") // duplicate coalesces
	if got := p.Snapshot(); len(got) != 2 {
		t.Fatalf("snapshot = %v, want 2 names", got)
	}
	got := p.Drain()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("drain = %v, want [a b] sorted", got)
	}
	if again := p.Drain(); again != nil {
		t.Fatalf("second drain = %v, want nil", again)
	}
	// Drain consumed the signal: C must not report stale wake-ups.
	select {
	case <-p.C():
		t.Fatal("C fired after drain with nothing pending")
	default:
	}
	// And a fresh Add signals again.
	p.Add("c")
	select {
	case <-p.C():
	default:
		t.Fatal("C did not fire after Add")
	}
}

func TestWebhookScheduler_FirstCycleIsFullSweep(t *testing.T) {
	s, err := NewWebhookScheduler(NewPendingSet(), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if opts := s.NextOptions(); opts.Names != nil {
		t.Fatalf("first cycle Names = %v, want nil (full sweep)", opts.Names)
	}
}

func TestWebhookScheduler_EventScopedCycle(t *testing.T) {
	p := NewPendingSet()
	s, _ := NewWebhookScheduler(p, time.Hour, 0)
	s.NextOptions() // consume the initial full sweep
	p.Add("repo-b")
	opts := s.NextOptions()
	if len(opts.Names) != 1 || opts.Names[0] != "repo-b" {
		t.Fatalf("Names = %v, want [repo-b]", opts.Names)
	}
}

func TestWebhookScheduler_EventWakesWait(t *testing.T) {
	p := NewPendingSet()
	s, _ := NewWebhookScheduler(p, time.Hour, 0)
	s.NextOptions()

	done := make(chan error, 1)
	go func() { done <- s.Wait(context.Background()) }()
	p.Add("repo-a")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not wake on a pending event")
	}
}

func TestWebhookScheduler_DebounceCoalescesBurst(t *testing.T) {
	p := NewPendingSet()
	s, _ := NewWebhookScheduler(p, time.Hour, 200*time.Millisecond)
	s.NextOptions()

	p.Add("repo-a")
	go func() {
		time.Sleep(50 * time.Millisecond)
		p.Add("repo-b")
	}()
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := s.NextOptions()
	if len(opts.Names) != 2 {
		t.Fatalf("Names = %v, want both repos coalesced into one cycle", opts.Names)
	}
}

func TestWebhookScheduler_SweepDeadlineSurvivesEventTraffic(t *testing.T) {
	p := NewPendingSet()
	s, _ := NewWebhookScheduler(p, 50*time.Millisecond, 0)
	s.NextOptions() // full sweep, deadline = now+50ms

	time.Sleep(60 * time.Millisecond)
	p.Add("repo-a")
	// Deadline passed: even though an event is pending, the cycle must be a
	// full sweep, and it consumes the pending name (the sweep covers it).
	if opts := s.NextOptions(); opts.Names != nil {
		t.Fatalf("Names = %v, want nil (sweep overdue)", opts.Names)
	}
	if got := p.Drain(); got != nil {
		t.Fatalf("sweep should have consumed pending names, got %v", got)
	}
	// Wait must return immediately when the deadline is already behind us.
	s2, _ := NewWebhookScheduler(NewPendingSet(), time.Hour, 0)
	start := time.Now()
	if err := s2.Wait(context.Background()); err != nil { // nextSweep zero = due
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Wait blocked although the sweep was already due")
	}
}

func TestWebhookScheduler_WaitHonorsContext(t *testing.T) {
	p := NewPendingSet()
	s, _ := NewWebhookScheduler(p, time.Hour, 0)
	s.NextOptions()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Wait(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait ignored ctx cancellation")
	}
}

// recordingSyncer captures which repos each cycle synced, in order.
type recordingSyncer struct {
	mu     sync.Mutex
	synced []string
}

func (s *recordingSyncer) Sync(_ context.Context, r Repository, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synced = append(s.synced, r.Name)
	return "deadbeef", nil
}

func (s *recordingSyncer) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.synced...)
}

var errStopTest = errors.New("stop test scheduler")

// stopAfterScheduler lets N cycles run, then stops RunForever by returning an
// error from Wait.
type stopAfterScheduler struct{ waits, max int }

func (s *stopAfterScheduler) Wait(context.Context) error {
	s.waits++
	if s.waits >= s.max {
		return errStopTest
	}
	return nil
}

func TestRunForeverDynamic_ScopesEachCycle(t *testing.T) {
	syncer := &recordingSyncer{}
	orch := testOrchestrator(threeRepos(), nil)
	orch.Syncer = syncer

	calls := 0
	optsFn := func() Options {
		calls++
		if calls == 1 {
			return Options{Names: []string{"repo-b"}} // webhook-style targeted cycle
		}
		return Options{} // full sweep
	}
	err := orch.RunForeverDynamic(context.Background(), optsFn, &stopAfterScheduler{max: 2})
	if !errors.Is(err, errStopTest) {
		t.Fatalf("RunForeverDynamic = %v, want errStopTest", err)
	}
	want := []string{"repo-b", "repo-a", "repo-b", "repo-c"}
	if len(syncer.synced) != len(want) {
		t.Fatalf("synced %v, want %v", syncer.synced, want)
	}
	for i := range want {
		if syncer.synced[i] != want[i] {
			t.Fatalf("synced %v, want %v", syncer.synced, want)
		}
	}
}

// TestWebhookEndToEnd_DeliveryDrivesTargetedCycle wires the real pieces
// together - HTTP server, HMAC-verified handler, pending set, webhook
// scheduler, orchestrator loop - and drives them with a genuine HTTP POST
// shaped exactly like a GitHub push delivery. The startup full sweep must
// cover all repos; the delivery must then trigger a cycle that syncs only
// the pushed repo.
func TestWebhookEndToEnd_DeliveryDrivesTargetedCycle(t *testing.T) {
	repos := []Repository{
		{Name: "repo-a", URL: "https://github.com/org/repo-a.git", Branch: "main"},
		{Name: "repo-b", URL: "https://github.com/org/repo-b.git", Branch: "main"},
		{Name: "repo-c", URL: "https://github.com/org/repo-c.git", Branch: "main"},
	}
	syncer := &recordingSyncer{}
	orch := testOrchestrator(repos, nil)
	orch.Syncer = syncer

	pending := NewPendingSet()
	sched, err := NewWebhookScheduler(pending, time.Hour, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewGitHubWebhookHandler(fakeSource{repos: repos}, testSecret, pending, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = orch.RunForeverDynamic(ctx, sched.NextOptions, sched)
	}()

	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (synced so far: %v)", desc, syncer.list())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("startup full sweep", func() bool { return len(syncer.list()) == 3 })

	body := pushBody(t, "https://github.com/org/repo-b.git", "refs/heads/main")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhook/github", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "e2e-delivery-1")
	req.Header.Set("X-Hub-Signature-256", sign(testSecret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery got %d, want 202", resp.StatusCode)
	}

	waitFor("webhook-triggered cycle", func() bool { return len(syncer.list()) == 4 })
	if got := syncer.list(); got[3] != "repo-b" {
		t.Fatalf("webhook cycle synced %q, want only repo-b (full log: %v)", got[3], got)
	}

	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator loop did not stop on ctx cancel")
	}
	// No stray extra cycles: exactly 3 (sweep) + 1 (targeted).
	if got := syncer.list(); len(got) != 4 {
		t.Fatalf("expected exactly 4 syncs, got %v", got)
	}
}

func TestStatusHandler(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Set(RepoState{
		Name:              "repo-a",
		LastStatus:        StatusSuccess,
		LastIndexedCommit: "deadbeef",
		LastIndexedAt:     now,
		LastAttemptAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSet()
	pending.Add("repo-b")

	h := NewStatusHandler(fakeSource{repos: threeRepos()}, store, pending)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	var resp struct {
		GeneratedAt  time.Time `json:"generated_at"`
		Repositories []struct {
			Name              string `json:"name"`
			LastStatus        string `json:"last_status"`
			LastIndexedCommit string `json:"last_indexed_commit"`
			PendingReindex    bool   `json:"pending_reindex"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
	}
	if resp.GeneratedAt.IsZero() {
		t.Fatal("generated_at missing")
	}
	if len(resp.Repositories) != 3 {
		t.Fatalf("got %d repositories, want 3 (configured-but-never-indexed repos must appear)", len(resp.Repositories))
	}
	// Sorted by name: repo-a, repo-b, repo-c.
	a, b := resp.Repositories[0], resp.Repositories[1]
	if a.Name != "repo-a" || a.LastStatus != "success" || a.LastIndexedCommit != "deadbeef" || a.PendingReindex {
		t.Fatalf("repo-a status wrong: %+v", a)
	}
	if b.Name != "repo-b" || !b.PendingReindex {
		t.Fatalf("repo-b should show pending_reindex: %+v", b)
	}
}
