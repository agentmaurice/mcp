package shared

import (
	"errors"
	"testing"
)

func TestIsTransientBrowserError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "page load shutdown",
			err:  ErrBrowserAction("navigate", errors.New("page load error Shutdown")),
			want: true,
		},
		{
			name: "missing execution context",
			err:  ErrBrowserAction("navigate", errors.New("Cannot find default execution context (-32000)")),
			want: true,
		},
		{
			name: "target closed",
			err:  errors.New("target closed"),
			want: true,
		},
		{
			name: "service unavailable",
			err:  ErrServiceUnavailable("browser"),
			want: true,
		},
		{
			name: "validation",
			err:  ErrValidation("invalid selector"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransientBrowserError(tc.err); got != tc.want {
				t.Fatalf("IsTransientBrowserError() = %v, want %v", got, tc.want)
			}
		})
	}
}
