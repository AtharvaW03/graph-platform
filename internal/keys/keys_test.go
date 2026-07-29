package keys

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateShape(t *testing.T) {
	plain, hash, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, Prefix) {
		t.Errorf("key %q missing prefix %q", plain, Prefix)
	}
	if len(plain) != len(Prefix)+rawBytes*2 {
		t.Errorf("key length = %d, want %d", len(plain), len(Prefix)+rawBytes*2)
	}
	if hash != HashKey(plain) {
		t.Error("returned hash does not match HashKey(plaintext)")
	}
	plain2, _, _ := generate()
	if plain == plain2 {
		t.Error("two generated keys are identical")
	}
}

func TestIsUserKey(t *testing.T) {
	if !IsUserKey(Prefix + "abc") {
		t.Error("prefixed credential not recognized as user key")
	}
	if IsUserKey("some-static-shared-token") {
		t.Error("static token misrecognized as user key")
	}
	if IsUserKey("") {
		t.Error("empty credential misrecognized as user key")
	}
}

func TestMonthlyExpiry(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			"mid-month",
			time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC),
			time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			"first instant of a month still expires next month",
			time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			"last moment of a month",
			time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC),
			time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			"December rolls into January of next year",
			time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			"non-UTC input normalized to UTC boundary",
			time.Date(2026, 7, 31, 23, 0, 0, 0, time.FixedZone("IST", 5*3600+1800)),
			time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := monthlyExpiry(tc.now); !got.Equal(tc.want) {
				t.Errorf("monthlyExpiry(%v) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

func TestKeyActive(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	revoked := now.Add(-time.Hour)
	cases := []struct {
		name string
		k    Key
		want bool
	}{
		{"live", Key{ExpiresAt: now.Add(time.Hour)}, true},
		{"expired", Key{ExpiresAt: now.Add(-time.Minute)}, false},
		{"revoked", Key{ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.k.Active(now); got != tc.want {
				t.Errorf("Active = %v, want %v", got, tc.want)
			}
		})
	}
}
