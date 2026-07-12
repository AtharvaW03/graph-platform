package index

import "time"

// progressTickInterval is how often a long-running foreground stage
// (the graphify subprocess, the extractor pass) reports that it's still
// alive. Chosen to stay quiet on the common case - most repos finish well
// inside it - while still bounding how long an operator can watch a silent
// terminal before wondering if the process is hung.
const progressTickInterval = 30 * time.Second

// startProgressTicker calls emit(elapsed) every interval until the returned
// stop func is called. The first call happens after one full interval, not
// immediately - a stage that finishes inside interval never emits anything.
// stop blocks until the ticker goroutine has actually exited, so nothing
// fires after the caller considers the stage done.
func startProgressTicker(interval time.Duration, emit func(elapsed time.Duration)) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				emit(time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() {
		close(stopCh)
		<-done
	}
}
