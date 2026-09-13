package mcp

import "testing"

func TestScanDocumentEmailClassificationAndIgnoreEntities(t *testing.T) {
	ignore := newScanIgnoreEntities(
		[]string{"dan.hayot@neurones.net"},
		[]string{"+33 6 23 89 00 51"},
		[]string{"Dan Hayot"},
	)

	doc := scanDocumentInput{
		DocumentID: "doc1",
		Title:      "CV Offre 18230",
		Metadata: map[string]interface{}{
			"file_name": "axones-hya___6900c6b87ed34.pdf",
		},
		Chunks: []*repositoryChunkView{
			{
				ID:   "chunk1",
				Text: "Hatem\nContact: hatem@gmail.com\nInterlocuteur: dan.hayot@neurones.net\nPhone: 06 12 34 56 78\nCommercial: 06 23 89 00 51",
			},
		},
	}

	result := scanDocumentPII(doc, "pii_strict", true, false, 20, ignore)

	summary := result["scan_summary"].(map[string]interface{})
	counts := summary["counts_by_type"].(map[string]int)
	ignored := summary["ignored_by_type"].(map[string]int)

	if counts["personal_email"] != 1 {
		t.Fatalf("expected one personal_email, got %#v", counts)
	}
	if counts["personal_phone_number"] != 1 {
		t.Fatalf("expected one personal_phone_number, got %#v", counts)
	}
	if ignored["ignored_email"] != 1 {
		t.Fatalf("expected ignored commercial email, got %#v", ignored)
	}
	if ignored["ignored_phone"] != 1 {
		t.Fatalf("expected ignored commercial phone, got %#v", ignored)
	}
}

func TestScanDocumentDetectsProfessionalEmailFromCorporateDomain(t *testing.T) {
	ignore := newScanIgnoreEntities(
		[]string{"dan.hayot@neurones.net"},
		nil,
		nil,
	)

	doc := scanDocumentInput{
		DocumentID: "doc2",
		Title:      "CV Offre 22023",
		Chunks: []*repositoryChunkView{
			{
				ID:   "chunk1",
				Text: "Contact professionnel: foued.benali@geodis.com",
			},
		},
	}

	result := scanDocumentPII(doc, "pii_strict", true, false, 20, ignore)
	summary := result["scan_summary"].(map[string]interface{})
	counts := summary["counts_by_type"].(map[string]int)

	if counts["professional_email"] != 1 {
		t.Fatalf("expected one professional_email, got %#v", counts)
	}
	if counts["personal_email"] != 0 {
		t.Fatalf("expected zero personal_email, got %#v", counts)
	}
}

func TestScanDocumentIgnoresCommercialNameAndDetectsFilenameName(t *testing.T) {
	ignore := newScanIgnoreEntities(nil, nil, []string{"Dan Hayot"})

	doc := scanDocumentInput{
		DocumentID: "doc3",
		Title:      "CV Offre 24326",
		Metadata: map[string]interface{}{
			"file_name": "walid-winside-developpeur-senior-fullstack___6927091c2fa53.pdf",
		},
		Chunks: []*repositoryChunkView{
			{
				ID:   "chunk1",
				Text: "Dan Hayot\nDeveloppeur senior\n...",
			},
		},
	}

	result := scanDocumentPII(doc, "pii_strict", true, false, 20, ignore)
	summary := result["scan_summary"].(map[string]interface{})
	counts := summary["counts_by_type"].(map[string]int)
	ignored := summary["ignored_by_type"].(map[string]int)

	if ignored["contact_name"] == 0 {
		t.Fatalf("expected commercial name to be ignored, got %#v", ignored)
	}
	if counts["name_in_file"] == 0 {
		t.Fatalf("expected filename name detection, got %#v", counts)
	}
}
