package index

import (
	"fmt"
	"io"
	"os"
	"time"
)

// isTerminal reports whether w is an interactive character device (a real
// terminal) rather than a pipe or file. It gates every animated/colored
// output path: piped or captured contexts - the daemon under Docker/systemd,
// CI, a log aggregator - return false and fall back to plain newline-per-event
// logging, so production log streams never carry carriage returns or ANSI
// escapes. No external dependency: a char-device stat is the classic
// pre-x/term terminal check.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// spinnerFrames is a smooth braille cycle - the animated "it's still working"
// indicator the user sees during a long extraction.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const spinnerInterval = 100 * time.Millisecond

// startSpinner shows a live, in-place progress indicator for a long-running
// stage when out is an interactive terminal: "⠋ label (12s)", redrawn every
// ~100ms with the frame cycling and the elapsed seconds ticking up. The
// returned stop func erases the line and blocks until the animator goroutine
// has exited, so nothing draws after the caller considers the stage done.
//
// When out is NOT a terminal it degrades to startProgressTicker's behavior: a
// newline-terminated "label (Ns)" every tickEvery, so captured logs stay one
// line per event and carry no carriage returns. Same contract either way -
// call stop exactly once.
func startSpinner(out io.Writer, label string, tickEvery time.Duration) (stop func()) {
	if !isTerminal(out) {
		return startProgressTicker(tickEvery, func(elapsed time.Duration) {
			fmt.Fprintf(out, "%s (%s)\n", label, elapsed)
		})
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(spinnerInterval)
		defer ticker.Stop()
		frame := 0
		draw := func() {
			elapsed := time.Since(start).Round(time.Second)
			// \r returns to column 0; \033[K erases to end of line so a shorter
			// redraw never leaves stale characters behind.
			fmt.Fprintf(out, "\r%c %s (%s)\033[K", spinnerFrames[frame], label, elapsed)
		}
		draw()
		for {
			select {
			case <-stopCh:
				fmt.Fprint(out, "\r\033[K") // erase the line for the next logger output
				return
			case <-ticker.C:
				frame = (frame + 1) % len(spinnerFrames)
				draw()
			}
		}
	}()
	return func() {
		close(stopCh)
		<-done
	}
}

// ANSI SGR codes for the colorized result/summary lines.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiGray   = "\033[90m"
)

// colorizer wraps text in ANSI color, but only when output is an interactive
// terminal and the NO_COLOR convention (https://no-color.org) is not set.
// Off, wrap returns its input unchanged, so piped/captured output and
// color-averse environments get clean plain text.
type colorizer struct{ on bool }

func newColorizer(w io.Writer) colorizer {
	return colorizer{on: isTerminal(w) && os.Getenv("NO_COLOR") == ""}
}

func (c colorizer) wrap(code, s string) string {
	if !c.on {
		return s
	}
	return code + s + ansiReset
}
