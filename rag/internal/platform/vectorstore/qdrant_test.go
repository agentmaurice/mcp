package vectorstore

import "testing"

func TestMetadataValueToString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input interface{}
		want  string
	}{
		{name: "string", input: "18224", want: "18224"},
		{name: "int", input: 18224, want: "18224"},
		{name: "int64", input: int64(3157), want: "3157"},
		{name: "float64 whole", input: 18224.0, want: "18224"},
		{name: "float64 decimal", input: 18.5, want: "18.5"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := metadataValueToString(tt.input)
			if got != tt.want {
				t.Fatalf("metadataValueToString(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
