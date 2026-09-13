package mcp

import (
	"context"
	"testing"
)

func TestParseCompareDocumentsAnswerJSON(t *testing.T) {
	answer := `{"overall_similarity": 0.72, "same_person_likelihood": 0.18, "duplicate_likelihood": 0.12, "decision_hint": "distinct", "shared_signals": ["stack Java/React proche"], "conflicting_signals": ["noms differents"], "justification": "Les profils ont un stack proche mais decrivent des parcours differents."}`

	overall, samePerson, duplicate, decision, shared, conflicting, justification := parseCompareDocumentsAnswer(answer)

	if overall != 0.72 {
		t.Fatalf("expected overall 0.72, got %v", overall)
	}
	if samePerson != 0.18 {
		t.Fatalf("expected same person 0.18, got %v", samePerson)
	}
	if duplicate != 0.12 {
		t.Fatalf("expected duplicate 0.12, got %v", duplicate)
	}
	if decision != "distinct" {
		t.Fatalf("unexpected decision: %q", decision)
	}
	if len(shared) != 1 || shared[0] != "stack Java/React proche" {
		t.Fatalf("unexpected shared signals: %#v", shared)
	}
	if len(conflicting) != 1 || conflicting[0] != "noms differents" {
		t.Fatalf("unexpected conflicting signals: %#v", conflicting)
	}
	if justification == "" {
		t.Fatalf("expected justification")
	}
}

func TestParseCompareDocumentsAnswerNormalizesPercentages(t *testing.T) {
	answer := `{"overall_similarity": 81, "same_person_likelihood": 24, "duplicate_likelihood": 19, "decision_hint": "related_but_distinct", "shared_signals": [], "conflicting_signals": [], "justification": "Pourcentages entiers."}`

	overall, samePerson, duplicate, _, _, _, _ := parseCompareDocumentsAnswer(answer)

	if overall != 0.81 {
		t.Fatalf("expected overall 0.81, got %v", overall)
	}
	if samePerson != 0.24 {
		t.Fatalf("expected same person 0.24, got %v", samePerson)
	}
	if duplicate != 0.19 {
		t.Fatalf("expected duplicate 0.19, got %v", duplicate)
	}
}

func TestCompareExtractedDocuments(t *testing.T) {
	docA := map[string]interface{}{
		"document_id": "docA",
		"title":       "CV Offre 18580",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18580",
		},
		"content": "Developpeur Java React avec missions banque et assurance chez Client A et Client B.",
	}
	docB := map[string]interface{}{
		"document_id": "docB",
		"title":       "CV Offre 18886",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18886",
		},
		"content": "Developpeur Java React avec missions telecom et ecommerce chez Client C et Client D.",
	}

	result, err := compareExtractedDocuments(
		context.Background(),
		&mockScoreLLM{
			answer: `{"overall_similarity": 0.67, "same_person_likelihood": 0.22, "duplicate_likelihood": 0.18, "decision_hint": "related_but_distinct", "shared_signals": ["stack Java React"], "conflicting_signals": ["missions differentes", "clients differents"], "justification": "Les stacks se ressemblent mais les experiences decrites ne correspondent pas au meme candidat."}`,
		},
		docA,
		docB,
		700,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["document_id_a"] != "docA" || result["document_id_b"] != "docB" {
		t.Fatalf("unexpected document ids: %#v", result)
	}
	if floatValue(result["duplicate_likelihood"]) != 0.18 {
		t.Fatalf("unexpected duplicate likelihood: %#v", result["duplicate_likelihood"])
	}
	if result["decision_hint"] != "related_but_distinct" {
		t.Fatalf("unexpected decision hint: %#v", result["decision_hint"])
	}
	shared, ok := result["shared_signals"].([]string)
	if !ok || len(shared) != 1 {
		t.Fatalf("unexpected shared signals payload: %#v", result["shared_signals"])
	}
	deterministic, ok := result["deterministic"].(compareDeterministicMetrics)
	if !ok {
		t.Fatalf("unexpected deterministic payload type: %#v", result["deterministic"])
	}
	if deterministic.TextJaccard <= 0 {
		t.Fatalf("expected positive text jaccard, got %#v", deterministic.TextJaccard)
	}
}
