package index

import (
	"context"
	"log"
	"time"
)

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
}

// Run ticks every Interval, calling Renew and reacting to failures, until ctx
// is canceled or the heartbeat gives up (OnFatal fires, then Run returns).
// Call it in its own goroutine.
func (h *LeaseHeartbeat) Run(ctx context.Context) {
	maxFailures := h.MaxConsecutiveFailures
	if maxFailures <= 0 {
		maxFailures = 3
	}
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := h.Renew(ctx)
			if err == nil {
				failures = 0
				continue
			}
			if h.IsLost != nil && h.IsLost(err) {
				h.Log.Printf("lease heartbeat: confirmed loss: %v", err)
				h.OnFatal(err)
				return
			}
			failures++
			h.Log.Printf("lease heartbeat: renewal failed (%d/%d consecutive, treating as transient): %v", failures, maxFailures, err)
			if failures >= maxFailures {
				h.Log.Printf("lease heartbeat: %d consecutive failures, giving up: %v", maxFailures, err)
				h.OnFatal(err)
				return
			}
		}
	}
}
