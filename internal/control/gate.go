package control

import "context"

// Paused implements index.PauseGate: it resolves the stored state against
// the current clock, so a timed pause ("hold until 23:00") lapses on its
// own without anyone calling Resume.
func (s *Store) Paused(ctx context.Context) (bool, string, error) {
	st, err := s.Get(ctx)
	if err != nil {
		return false, "", err
	}
	if !st.PausedNow(s.now()) {
		return false, "", nil
	}
	return true, st.Describe(), nil
}
