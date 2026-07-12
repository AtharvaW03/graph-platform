package index

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProgressTicker_EmitsOnSchedule(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	stop := startProgressTicker(10*time.Millisecond, func(elapsed time.Duration) {
		mu.Lock()
		fmt.Fprintf(&buf, "[repo] still running (%s elapsed)\n", elapsed)
		mu.Unlock()
	})

	time.Sleep(55 * time.Millisecond) // ~5 ticks at a 10ms interval
	stop()

	mu.Lock()
	defer mu.Unlock()
	lines := bytes.Count(buf.Bytes(), []byte("\n"))
	if lines < 3 {
		t.Errorf("emitted %d lines in ~55ms at a 10ms interval, want at least 3:\n%s", lines, buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("[repo] still running (")) {
		t.Errorf("emitted output missing the expected line shape:\n%s", buf.String())
	}
}

func TestProgressTicker_NeverFiresBeforeFirstInterval(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	stop := startProgressTicker(time.Hour, func(elapsed time.Duration) {
		mu.Lock()
		fmt.Fprintln(&buf, elapsed)
		mu.Unlock()
	})
	time.Sleep(20 * time.Millisecond)
	stop()

	mu.Lock()
	defer mu.Unlock()
	if buf.Len() != 0 {
		t.Errorf("emitted output before the first interval elapsed, want none:\n%s", buf.String())
	}
}

func TestProgressTicker_StopIsImmediateAndFinal(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	stop := startProgressTicker(5*time.Millisecond, func(elapsed time.Duration) {
		mu.Lock()
		fmt.Fprintln(&buf, elapsed)
		mu.Unlock()
	})
	time.Sleep(12 * time.Millisecond)
	stop()

	mu.Lock()
	afterStop := buf.Len()
	mu.Unlock()

	time.Sleep(30 * time.Millisecond) // long enough for several more ticks, if it were still running
	mu.Lock()
	defer mu.Unlock()
	if buf.Len() != afterStop {
		t.Errorf("output grew after stop(): %q -> %q", buf.String()[:afterStop], buf.String())
	}
}
