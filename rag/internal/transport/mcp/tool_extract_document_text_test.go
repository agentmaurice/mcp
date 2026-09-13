package mcp

import (
	"encoding/json"
	"testing"
)

func TestMetadataMatchesFiltersSupportsNumericAndAliases(t *testing.T) {
	metadata := map[string]interface{}{
		"offreId":        float64(18889),
		"consultationId": json.Number("3157"),
		"trigramme":      "GAE",
	}

	if !metadataMatchesFilters(metadata, map[string]string{
		"offre_id":        "18889",
		"consultation_id": "3157",
		"trigramme":       "GAE",
	}) {
		t.Fatalf("expected metadata to match filters")
	}
}

func TestMetadataMatchesFiltersRejectsMismatches(t *testing.T) {
	metadata := map[string]interface{}{
		"offre_id":  "18889",
		"trigramme": "GAE",
	}

	if metadataMatchesFilters(metadata, map[string]string{"offre_id": "18891"}) {
		t.Fatalf("expected metadata filter mismatch")
	}
}
