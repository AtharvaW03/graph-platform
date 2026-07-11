package index

import (
	"context"
	"log"
	"sync"
	"time"
)

// renewCallTimeout caps a single Renew call, so a hung call (e.g. a dead TCP
// connection that never errors) can't silently block the heartbeat past the
// lease TTL. Interval is usually smaller than this and wins; this is the
// ceiling for a long Interval.
const renewCallTimeout = 30 * time.Second

// LeaseHeartbeat renews the writer lease on a fixed tick, independent of the
// orchestrator's per-repo renewal (Orchestrator.Lease). It exists to close a
// gap the per-repo renewal can't: a single repo's graphify run can itself
// outlive the lease TTL, and the orchestrator only gets a chance to renew
// between repos. Both renewal paths call the same owner-guarded RenewLease,
// so their calls never conflict - renewing early just extends the same
// deadline again.
//
// Kept here (internal/index) rather than in cmd/indexer so the tick/failure
// logic is unit-testable with a fake Renew func, no live Neo4j required.
type LeaseHeartbeat struct {
	// Renew is called every Interval. Required.
	Renew func(ctx context.Context) error
	// Interval between renewal attempts. Required, must be > 0.
	Interval time.Duration
	Log      *log.Logger

	// IsLost reports whether err means the lease is confirmedly gone (someone
	// else claimed it), as opposed to a transient failure (network blip). A
	// confirmed loss is fatal immediately, bypassing MaxConsecutiveFailures -
	// retrying against a lease someone else holds would just race them. Nil
	// means no error is ever treated as a confirmed loss, only counted toward
	// MaxConsecutiveFailures.
	IsLost func(err error) bool

	// OnFatal is called exactly once, synchronously from Run's goroutine, when
	// the heartbeat gives up: a confirmed loss, or MaxConsecutiveFailures
	// transient errors in a row. Run returns immediately after. The caller
	// typically cancels the process's working context here so in-flight
	// writes stop.
	OnFatal func(err error)

	// MaxConsecutiveFailures transient (non-lost) renewal errors in a row are
	// tolerated before giving up too - a renewal that keeps failing is more
	// likely a dead connection than a blip. Zero means the default: 3.
	MaxConsecutiveFailures int

	mu    sync.Mutex
	fatal error
}

// FatalErr returns the error that made Run give up, or nil if Run is still
// ticking (or never started, or exited cleanly via ctx cancellation without
// ever failing). Safe to call concurrently with Run - this is the intended
// way for a caller to check, after its own work finishes, whether the lease
// was actually lost partway through: ctx cancellation alone doesn't say why
// it was canceled, but FatalErr is only ever set on a genuine give-up.
func (h *LeaseHeartbeat) FatalErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fatal
}

// Run ticks every Interval, calling Renew and reacting to failures, until ctx
// is canceled or the heartbeat gives up (OnFatal fires, then Run returns).
// Call it in its own goroutine.
func (h *LeaseHeartbeat) Run(ctx context.Context) {
	if h.Interval <= 0 {
		// Should be unreachable - the CLI validates --lease-ttl before this is
		// ever constructed - but time.NewTicker panics on a non-positive
		// duration, so this is the difference between a log line and a crash.
		h.Log.Printf("lease heartbeat: invalid interval %s, not starting", h.Interval)
		return
	}
	maxFailures := h.MaxConsecutiveFailures
	if maxFailures <= 0 {
		maxFailures = 3
	}
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()

	giveUp := func(err error) {
		h.mu.Lock()
		h.fatal = err
		h.mu.Unlock()
		h.OnFatal(err)
	}

	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, min(h.Interval, renewCallTimeout))
			err := h.Renew(renewCtx)
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			if h.IsLost != nil && h.IsLost(err) {
				h.Log.Printf("lease heartbeat: confirmed loss: %v", err)
				giveUp(err)
				return
			}
			failures++
			h.Log.Printf("lease heartbeat: renewal failed (%d/%d consecutive, treating as transient): %v", failures, maxFailures, err)
			if failures >= maxFailures {
				h.Log.Printf("lease heartbeat: %d consecutive failures, giving up: %v", maxFailures, err)
				giveUp(err)
				return
			}
		}
	}
}
