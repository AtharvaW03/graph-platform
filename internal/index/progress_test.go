package index

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestIsTerminal_NonFileIsFalse(t *testing.T) {
	// A bytes.Buffer is not an *os.File, so it can never be a terminal - this
	// is the branch every piped/captured caller hits.
	if isTerminal(&bytes.Buffer{}) {
		t.Error("isTerminal(*bytes.Buffer) = true, want false")
	}
}

func TestColorizerWrap(t *testing.T) {
	off := colorizer{on: false}
	if got := off.wrap(ansiGreen, "ok"); got != "ok" {
		t.Errorf("off.wrap = %q, want unchanged %q", got, "ok")
	}

	on := colorizer{on: true}
	got := on.wrap(ansiGreen, "ok")
	if !strings.HasPrefix(got, ansiGreen) || !strings.HasSuffix(got, ansiReset) || !strings.Contains(got, "ok") {
		t.Errorf("on.wrap = %q, want green-wrapped %q", got, "ok")
	}
}

func TestNewColorizer_NonTerminalIsOff(t *testing.T) {
	// Not a terminal -> color must be off regardless of NO_COLOR, so piped
	// output never carries ANSI escapes.
	if newColorizer(&bytes.Buffer{}).on {
		t.Error("newColorizer(*bytes.Buffer).on = true, want false")
	}
}

func TestStartSpinner_NonTTYFallbackIsPlain(t *testing.T) {
	// A non-terminal writer takes the ticker path: newline-terminated lines,
	// no carriage returns and no ANSI escapes, so captured logs stay clean.
	var buf bytes.Buffer
	stop := startSpinner(&buf, "auth-service: extracting", 15*time.Millisecond)
	time.Sleep(55 * time.Millisecond)
	stop() // blocks until the goroutine exits; safe to read buf after this

	out := buf.String()
	if !strings.Contains(out, "auth-service: extracting") {
		t.Errorf("output %q missing the label", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("non-TTY output contains a carriage return: %q", out)
	}
	if strings.ContainsRune(out, '\033') {
		t.Errorf("non-TTY output contains an ANSI escape: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("non-TTY output is not newline-terminated: %q", out)
	}
}
