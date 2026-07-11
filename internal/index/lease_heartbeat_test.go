package index

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLeaseHeartbeat_RenewsOnSchedule(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return nil
		},
		Interval: 10 * time.Millisecond,
		Log:      discardLogger(),
		OnFatal:  func(err error) { t.Errorf("OnFatal should not be called for a healthy renewer, got %v", err) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	time.Sleep(65 * time.Millisecond) // ~6 ticks at 10ms
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Errorf("renew called %d times in ~65ms at a 10ms interval, want at least 3", calls)
	}
}

func TestLeaseHeartbeat_ConfirmedLossIsFatalImmediately(t *testing.T) {
	errLost := errors.New("lease lost (test)")
	var mu sync.Mutex
	calls := 0
	fatalCh := make(chan error, 1)

	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return errLost
		},
		Interval:               10 * time.Millisecond,
		Log:                    discardLogger(),
		IsLost:                 func(err error) bool { return errors.Is(err, errLost) },
		OnFatal:                func(err error) { fatalCh <- err },
		MaxConsecutiveFailures: 10, // must not matter - a confirmed loss is immediate
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	select {
	case err := <-fatalCh:
		if !errors.Is(err, errLost) {
			t.Errorf("OnFatal err = %v, want wrapping the confirmed-loss error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnFatal was never called after a confirmed lease loss")
	}
	<-done // Run must return right after calling OnFatal

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("renew called %d times, want exactly 1 (fatal on the first confirmed loss, no retrying)", calls)
	}
}

func TestLeaseHeartbeat_TransientErrorsBelowMaxKeepTicking(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return errors.New("transient blip")
		},
		Interval:               10 * time.Millisecond,
		Log:                    discardLogger(),
		IsLost:                 func(err error) bool { return false }, // never a confirmed loss
		OnFatal:                func(err error) { t.Errorf("OnFatal should not fire below MaxConsecutiveFailures, got %v", err) },
		MaxConsecutiveFailures: 100,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	time.Sleep(55 * time.Millisecond) // ~5 ticks, well under the 100-failure cap
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Errorf("renew called %d times, want at least 3 - transient errors must not stop ticking", calls)
	}
}

func TestLeaseHeartbeat_MaxConsecutiveTransientFailuresIsFatal(t *testing.T) {
	transient := errors.New("transient blip")
	var mu sync.Mutex
	calls := 0
	fatalCh := make(chan error, 1)

	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 2 {
				return nil // one healthy renewal resets the streak
			}
			return transient
		},
		Interval:               5 * time.Millisecond,
		Log:                    discardLogger(),
		IsLost:                 func(err error) bool { return false },
		OnFatal:                func(err error) { fatalCh <- err },
		MaxConsecutiveFailures: 3,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	select {
	case err := <-fatalCh:
		if !errors.Is(err, transient) {
			t.Errorf("OnFatal err = %v, want wrapping the transient error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected OnFatal after 3 consecutive transient failures")
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	// call 1: fail (streak 1), call 2: ok (streak resets to 0),
	// call 3: fail (streak 1), call 4: fail (streak 2), call 5: fail (streak 3 -> fatal).
	if calls != 5 {
		t.Errorf("renew called %d times, want 5 (one success resets the streak, then 3 more to trip the cap)", calls)
	}
}

func TestLeaseHeartbeat_DefaultMaxConsecutiveFailures(t *testing.T) {
	// MaxConsecutiveFailures left at zero should default to 3, not "never fatal".
	var mu sync.Mutex
	calls := 0
	fatalCh := make(chan error, 1)

	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return errors.New("always fails")
		},
		Interval: 5 * time.Millisecond,
		Log:      discardLogger(),
		OnFatal:  func(err error) { fatalCh <- err },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	select {
	case <-fatalCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the default MaxConsecutiveFailures (3) to trip OnFatal")
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("renew called %d times, want 3 (the documented default)", calls)
	}
}

func TestLeaseHeartbeat_FatalErrSetOnGiveUp(t *testing.T) {
	errLost := errors.New("lease lost (test)")
	h := &LeaseHeartbeat{
		Renew:    func(context.Context) error { return errLost },
		Interval: 5 * time.Millisecond,
		Log:      discardLogger(),
		IsLost:   func(err error) bool { return errors.Is(err, errLost) },
		OnFatal:  func(error) {},
	}

	if got := h.FatalErr(); got != nil {
		t.Errorf("FatalErr before Run starts = %v, want nil", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	<-done

	got := h.FatalErr()
	if !errors.Is(got, errLost) {
		t.Errorf("FatalErr() = %v, want wrapping the confirmed-loss error", got)
	}
}

func TestLeaseHeartbeat_FatalErrNilWhenHealthy(t *testing.T) {
	h := &LeaseHeartbeat{
		Renew:    func(context.Context) error { return nil },
		Interval: 5 * time.Millisecond,
		Log:      discardLogger(),
		OnFatal:  func(err error) { t.Errorf("OnFatal should not fire, got %v", err) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if got := h.FatalErr(); got != nil {
		t.Errorf("FatalErr() after clean ctx cancellation = %v, want nil", got)
	}
}

func TestLeaseHeartbeat_InvalidIntervalLogsAndReturns(t *testing.T) {
	h := &LeaseHeartbeat{
		Renew: func(context.Context) error {
			t.Error("Renew should never be called with an invalid interval")
			return nil
		},
		Interval: 0,
		Log:      discardLogger(),
		OnFatal:  func(err error) { t.Errorf("OnFatal should not fire, got %v", err) },
	}

	done := make(chan struct{})
	go func() { h.Run(context.Background()); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly for Interval <= 0")
	}
}

func TestLeaseHeartbeat_RenewCallTimesOut(t *testing.T) {
	// Renew blocks on its ctx instead of returning - simulates a hung call
	// (dead connection that never errors on its own). The per-call deadline
	// must cut it off and count it as a transient failure, not hang forever.
	var mu sync.Mutex
	calls := 0
	h := &LeaseHeartbeat{
		Renew: func(ctx context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			<-ctx.Done()
			return ctx.Err()
		},
		Interval:               20 * time.Millisecond,
		Log:                    discardLogger(),
		IsLost:                 func(err error) bool { return false },
		MaxConsecutiveFailures: 2,
	}
	fatalCh := make(chan error, 1)
	h.OnFatal = func(err error) { fatalCh <- err }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	select {
	case err := <-fatalCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("OnFatal err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected the hung Renew calls to time out and eventually trip MaxConsecutiveFailures")
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("renew called %d times, want 2 (MaxConsecutiveFailures)", calls)
	}
}
