package index

import (
	"context"
	"fmt"
	"time"
)

// Scheduler decides when the next indexing cycle should start. The contract
// is intentionally narrow - one Wait method - so future cron-based or
// channel-triggered (webhook fire-now) variants drop in without touching
// the orchestrator. Wait MUST honor ctx cancellation.
type Scheduler interface {
	// Wait blocks until the next cycle should start, or returns ctx.Err()
	// when ctx is canceled. Returning nil means "fire now"; any non-nil
	// non-context error aborts RunForever.
	Wait(ctx context.Context) error
}

// IntervalScheduler fires on a fixed period. It is the default scheduler
// used when the operator passes --interval on the CLI.
type IntervalScheduler struct {
	d time.Duration
}

func NewIntervalScheduler(d time.Duration) (*IntervalScheduler, error) {
	if d <= 0 {
		return nil, fmt.Errorf("interval must be > 0")
	}
	return &IntervalScheduler{d: d}, nil
}

func (s *IntervalScheduler) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.d):
		return nil
	}
}

// WebhookScheduler fires a cycle when webhook deliveries mark
// repositories dirty, and also enforces a periodic full reconciliation
// sweep. GitHub webhook delivery is best-effort (failures are never
// retried), so the sweep bounds staleness: it fetches every configured repo
// and re-indexes any whose HEAD moved without a delivery.
//
// The sweep deadline is persistent across cycles, so steady webhook traffic
// cannot postpone the sweep.
//
// Wait and NextOptions must be called from the same goroutine (the
// orchestrator loop); PendingSet handles the cross-goroutine synchronization
// with the HTTP handlers.
type WebhookScheduler struct {
	pending    *PendingSet
	sweepEvery time.Duration
	// debounce is how long Wait lingers after the first pending signal so a
	// burst (a release train touching many repos at once) coalesces into one
	// cycle instead of N.
	debounce time.Duration

	nextSweep time.Time
}

func NewWebhookScheduler(pending *PendingSet, sweepEvery, debounce time.Duration) (*WebhookScheduler, error) {
	if pending == nil {
		return nil, fmt.Errorf("pending set is required")
	}
	if sweepEvery <= 0 {
		return nil, fmt.Errorf("sweep interval must be > 0")
	}
	if debounce < 0 {
		return nil, fmt.Errorf("debounce must be >= 0")
	}
	return &WebhookScheduler{pending: pending, sweepEvery: sweepEvery, debounce: debounce}, nil
}

func (s *WebhookScheduler) Wait(ctx context.Context) error {
	// nextSweep is advanced by NextOptions; the zero value makes the first
	// cycle a full sweep, so by the time Wait first runs it is always set.
	until := time.Until(s.nextSweep)
	if until <= 0 {
		return nil
	}
	sweep := time.NewTimer(until)
	defer sweep.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sweep.C:
		return nil
	case <-s.pending.C():
		if s.debounce > 0 {
			t := time.NewTimer(s.debounce)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
		return nil
	}
}

// NextOptions decides the scope of the cycle that is about to run: a full
// sweep when the sweep deadline has passed (which also consumes anything
// pending, since the sweep covers every repo), otherwise just the repos
// webhook deliveries queued. A wake-up with nothing pending falls through to
// full-sweep semantics (empty Names means "all"), which costs one round of
// cheap no-op fetches rather than a skipped cycle.
func (s *WebhookScheduler) NextOptions() Options {
	now := time.Now()
	if !now.Before(s.nextSweep) {
		s.nextSweep = now.Add(s.sweepEvery)
		s.pending.Drain()
		return Options{}
	}
	return Options{Names: s.pending.Drain()}
}
