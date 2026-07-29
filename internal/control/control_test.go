package control

import (
	"testing"
	"time"
)

func TestPausedNow(t *testing.T) {
	now := time.Date(2026, 7, 29, 21, 30, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	cases := []struct {
		name  string
		state State
		want  bool
	}{
		{"running", State{Paused: false}, false},
		{"paused indefinitely", State{Paused: true}, true},
		{"timed pause still in effect", State{Paused: true, PausedUntil: &future}, true},
		{"timed pause lapsed", State{Paused: true, PausedUntil: &past}, false},
		{"not paused with a stale until", State{Paused: false, PausedUntil: &future}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.PausedNow(now); got != tc.want {
				t.Errorf("PausedNow = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	until := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	if got := (State{}).Describe(); got != "running" {
		t.Errorf("running state describes as %q", got)
	}
	if got := (State{Paused: true, Actor: "ops@org"}).Describe(); got == "running" {
		t.Errorf("paused state describes as %q", got)
	}
	got := State{Paused: true, Actor: "ops@org", PausedUntil: &until}.Describe()
	if got == "running" || got == "" {
		t.Errorf("timed pause describes as %q", got)
	}
}
