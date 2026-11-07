package search

import (
	"reflect"
	"testing"
)

func TestExtractIdentifiers(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"AGENT-530", []string{"AGENT-530"}},
		{"MEM-G3 et calcul_taux_marge", []string{"MEM-G3", "calcul_taux_marge"}},
		{"version 2026.10.19-3015.", []string{"2026.10.19-3015"}},
		{"ExecContext timeout", nil},
		{"AgentMaurice AGENT-941", []string{"AGENT-941"}},
		{"pourquoi la tarification", nil},
		{"2026", nil},
		{"e-mail", nil},
		{"AGENT-530 AGENT-530", []string{"AGENT-530"}},
	}
	for _, tc := range cases {
		if got := ExtractIdentifiers(tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExtractIdentifiers(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
