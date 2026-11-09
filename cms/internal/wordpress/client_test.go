package wordpress

import (
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"120":                           120 * time.Second,
		"0":                             time.Hour,
		"-5":                            time.Hour,
		"999999999":                     time.Hour,
		"junk":                          time.Hour,
		"":                              time.Hour,
		"Sun, 08 Nov 2026 10:05:00 GMT": 5 * time.Minute,
		"Sun, 08 Nov 2026 09:00:00 GMT": time.Hour,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q: %v want %v", in, got, want)
		}
	}
}

func TestEndpoint(t *testing.T) {
	if Endpoint("page") != "pages" || Endpoint("post") != "posts" || Endpoint("") != "posts" {
		t.Fatal("endpoint mapping")
	}
}
