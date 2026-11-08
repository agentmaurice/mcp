package postiz

import (
	"testing"
	"time"
)

func TestParseRetryAfterBounded(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"120", 120 * time.Second},
		{"86400", 24 * time.Hour},
		{"86401", time.Hour},
		{"9223372036854775807", time.Hour},
		{"99999999999999999999999", time.Hour},
		{"-5", time.Hour},
		{"0", time.Hour},
		{"abc", time.Hour},
		{"", time.Hour},
		{"Sat, 07 Nov 2026 10:05:00 GMT", 5 * time.Minute},
		{"Sat, 07 Nov 2026 09:00:00 GMT", time.Hour},
		{"Wed, 07 Nov 2029 10:00:00 GMT", time.Hour},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in, now); got != c.want {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}
