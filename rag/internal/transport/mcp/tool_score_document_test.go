package mcp

import (
	"context"
	"strings"
	"testing"
)

type mockScoreLLM struct {
	answer string
	err    error
}

func (m *mockScoreLLM) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	return nil, nil
}

func (m *mockScoreLLM) GenerateAnswer(ctx context.Context, prompt string, maxTokens int) (string, error) {
	return m.answer, m.err
}

func TestParseScoreDocumentAnswerJSON(t *testing.T) {
	answer := `{"score": 87, "justification": "Tres bonne adequation.", "strengths": ["Kotlin", "AWS"], "gaps": ["React"], "required_skills_matched": ["Kotlin"], "required_skills_missing": ["Spring Boot"], "important_skills_matched": ["AWS"], "important_skills_missing": ["Dart"], "optional_skills_matched": ["React"], "seniority_assessment": "match"}`

	score, justification, strengths, gaps, requiredMatched, requiredMissing, importantMatched, importantMissing, optionalMatched, seniorityAssessment := parseScoreDocumentAnswer(answer)

	if score != 87 {
		t.Fatalf("expected score 87, got %d", score)
	}
	if justification != "Tres bonne adequation." {
		t.Fatalf("unexpected justification: %q", justification)
	}
	if len(strengths) != 2 || strengths[0] != "Kotlin" {
		t.Fatalf("unexpected strengths: %#v", strengths)
	}
	if len(gaps) != 1 || gaps[0] != "React" {
		t.Fatalf("unexpected gaps: %#v", gaps)
	}
	if len(requiredMatched) != 1 || requiredMatched[0] != "Kotlin" {
		t.Fatalf("unexpected required matched: %#v", requiredMatched)
	}
	if len(requiredMissing) != 1 || requiredMissing[0] != "Spring Boot" {
		t.Fatalf("unexpected required missing: %#v", requiredMissing)
	}
	if len(importantMatched) != 1 || importantMatched[0] != "AWS" {
		t.Fatalf("unexpected important matched: %#v", importantMatched)
	}
	if len(importantMissing) != 1 || importantMissing[0] != "Dart" {
		t.Fatalf("unexpected important missing: %#v", importantMissing)
	}
	if len(optionalMatched) != 1 || optionalMatched[0] != "React" {
		t.Fatalf("unexpected optional matched: %#v", optionalMatched)
	}
	if seniorityAssessment != "match" {
		t.Fatalf("unexpected seniority assessment: %q", seniorityAssessment)
	}
}

func TestParseScoreDocumentAnswerNestedJSONInJustification(t *testing.T) {
	answer := "{\"justification\":\"```json\\n{\\n  \\\"score\\\": 70,\\n  \\\"justification\\\": \\\"Profil partiel.\\\",\\n  \\\"strengths\\\": [\\\"SpringBoot\\\"],\\n  \\\"gaps\\\": [\\\"Kotlin\\\"],\\n  \\\"required_skills_matched\\\": [\\\"SpringBoot\\\"],\\n  \\\"required_skills_missing\\\": [\\\"Kotlin\\\"],\\n  \\\"important_skills_matched\\\": [],\\n  \\\"important_skills_missing\\\": [\\\"Dart\\\"],\\n  \\\"optional_skills_matched\\\": [],\\n  \\\"seniority_assessment\\\": \\\"match\\\"\\n}\\n```\"}"

	score, justification, strengths, gaps, requiredMatched, requiredMissing, importantMatched, importantMissing, optionalMatched, seniorityAssessment := parseScoreDocumentAnswer(answer)

	if score != 70 {
		t.Fatalf("expected score 70, got %d", score)
	}
	if justification != "Profil partiel." {
		t.Fatalf("unexpected justification: %q", justification)
	}
	if len(strengths) != 1 || strengths[0] != "SpringBoot" {
		t.Fatalf("unexpected strengths: %#v", strengths)
	}
	if len(gaps) != 1 || gaps[0] != "Kotlin" {
		t.Fatalf("unexpected gaps: %#v", gaps)
	}
	if len(requiredMatched) != 1 || requiredMatched[0] != "SpringBoot" {
		t.Fatalf("unexpected required matched: %#v", requiredMatched)
	}
	if len(requiredMissing) != 1 || requiredMissing[0] != "Kotlin" {
		t.Fatalf("unexpected required missing: %#v", requiredMissing)
	}
	if len(importantMatched) != 0 {
		t.Fatalf("unexpected important matched: %#v", importantMatched)
	}
	if len(importantMissing) != 1 || importantMissing[0] != "Dart" {
		t.Fatalf("unexpected important missing: %#v", importantMissing)
	}
	if len(optionalMatched) != 0 {
		t.Fatalf("unexpected optional matched: %#v", optionalMatched)
	}
	if seniorityAssessment != "match" {
		t.Fatalf("unexpected seniority assessment: %q", seniorityAssessment)
	}
}

func TestParseScoreDocumentAnswerRegexFallback(t *testing.T) {
	answer := "```json\n{\n  \"score\": 85,\n  \"justification\": \"Profil solide mais Dart absent.\",\n  \"strengths\": [\n    \"Kotlin\",\n    \"AWS\"\n  ],\n  \"gaps\": [\n    \"Dart\"\n  ]\n"

	score, justification, strengths, gaps, requiredMatched, requiredMissing, importantMatched, importantMissing, optionalMatched, seniorityAssessment := parseScoreDocumentAnswer(answer)

	if score != 85 {
		t.Fatalf("expected score 85, got %d", score)
	}
	if justification != "Profil solide mais Dart absent." {
		t.Fatalf("unexpected justification: %q", justification)
	}
	if strengths != nil || gaps != nil || requiredMatched != nil || requiredMissing != nil || importantMatched != nil || importantMissing != nil || optionalMatched != nil {
		t.Fatalf("expected regex fallback to leave lists nil")
	}
	if seniorityAssessment != "unknown" {
		t.Fatalf("unexpected seniority assessment: %q", seniorityAssessment)
	}
}

func TestTrimForScoringMarksTruncated(t *testing.T) {
	text, truncated := trimForScoring("abcdefghijklmnopqrstuvwxyz", 10)

	if !truncated {
		t.Fatalf("expected truncated to be true")
	}
	if text != "abcdefghij" {
		t.Fatalf("unexpected trimmed text: %q", text)
	}
}

func TestScoreExtractedDocument(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc1",
		"title":       "CV Offre 18224",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18224",
		},
		"content": "Kotlin, Spring Boot, AWS, React",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 91, "justification": "Profil tres adapte.", "strengths": ["Kotlin", "AWS"], "gaps": ["Aucune"], "required_skills_matched": ["Kotlin", "Spring Boot"], "required_skills_missing": [], "important_skills_matched": ["AWS"], "important_skills_missing": [], "optional_skills_matched": ["React"], "seniority_assessment": "match"}`,
		},
		doc,
		"Mission Kotlin AWS",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot"},
			ImportantSkills: []string{"AWS"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if intValue(match["score"]) != 91 {
		t.Fatalf("expected score 91, got %#v", match["score"])
	}
	if match["document_id"] != "doc1" {
		t.Fatalf("unexpected document id: %#v", match["document_id"])
	}
	strengths, ok := match["strengths"].([]string)
	if !ok || len(strengths) != 2 {
		t.Fatalf("unexpected strengths payload: %#v", match["strengths"])
	}
	requiredMissing, ok := match["required_skills_missing"].([]string)
	if !ok || len(requiredMissing) != 0 {
		t.Fatalf("unexpected required missing payload: %#v", match["required_skills_missing"])
	}
	if match["seniority_assessment"] != "match" {
		t.Fatalf("unexpected seniority assessment: %#v", match["seniority_assessment"])
	}
}

func TestScoreExtractedDocumentReconcilesRubricSkillsAndSeniority(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc2",
		"title":       "CV Offre 22447",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "22447",
		},
		"content": "7 ans d'experience en Kotlin Spring Boot AWS avec Flutter et React.",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 85, "justification": "Profil adapte.", "strengths": ["Kotlin"], "gaps": ["Flutter manque"], "required_skills_matched": ["Kotlin"], "required_skills_missing": ["Flutter"], "important_skills_matched": [], "important_skills_missing": ["Dart"], "optional_skills_matched": [], "seniority_assessment": "below"}`,
		},
		doc,
		"Mission full stack",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot", "AWS"},
			ImportantSkills: []string{"Dart"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requiredMatched, ok := match["required_skills_matched"].([]string)
	if !ok || len(requiredMatched) != 3 {
		t.Fatalf("unexpected required matched payload: %#v", match["required_skills_matched"])
	}
	requiredMissing, ok := match["required_skills_missing"].([]string)
	if !ok || len(requiredMissing) != 0 {
		t.Fatalf("unexpected required missing payload: %#v", match["required_skills_missing"])
	}
	requiredMentionedOnly, ok := match["required_skills_mentioned_only"].([]string)
	if !ok || len(requiredMentionedOnly) != 0 {
		t.Fatalf("unexpected required mentioned-only payload: %#v", match["required_skills_mentioned_only"])
	}
	importantMatched, ok := match["important_skills_matched"].([]string)
	if !ok || len(importantMatched) != 1 || importantMatched[0] != "Dart" {
		t.Fatalf("unexpected important matched payload: %#v", match["important_skills_matched"])
	}
	importantMentionedOnly, ok := match["important_skills_mentioned_only"].([]string)
	if !ok || len(importantMentionedOnly) != 0 {
		t.Fatalf("unexpected important mentioned-only payload: %#v", match["important_skills_mentioned_only"])
	}
	if match["seniority_assessment"] != "match" {
		t.Fatalf("unexpected seniority assessment: %#v", match["seniority_assessment"])
	}
}

func TestScoreExtractedDocumentIgnoresLLMSkillHallucinations(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc3",
		"title":       "CV Offre 18225",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18225",
		},
		"content": "5 ans d'experience en Java Spring Boot et Angular.",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 70, "justification": "Profil partiel.", "strengths": ["Spring Boot"], "gaps": ["Kotlin", "AWS"], "required_skills_matched": ["Kotlin", "Spring Boot", "AWS"], "required_skills_missing": [], "important_skills_matched": ["Dart"], "important_skills_missing": [], "optional_skills_matched": [], "seniority_assessment": "below"}`,
		},
		doc,
		"Mission full stack",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot", "AWS"},
			ImportantSkills: []string{"Dart"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requiredMatched, ok := match["required_skills_matched"].([]string)
	if !ok || len(requiredMatched) != 1 || requiredMatched[0] != "Spring Boot" {
		t.Fatalf("unexpected required matched payload: %#v", match["required_skills_matched"])
	}
	requiredMentionedOnly, ok := match["required_skills_mentioned_only"].([]string)
	if !ok || len(requiredMentionedOnly) != 0 {
		t.Fatalf("unexpected required mentioned-only payload: %#v", match["required_skills_mentioned_only"])
	}
	requiredMissing, ok := match["required_skills_missing"].([]string)
	if !ok || len(requiredMissing) != 2 {
		t.Fatalf("unexpected required missing payload: %#v", match["required_skills_missing"])
	}
	importantMatched, ok := match["important_skills_matched"].([]string)
	if !ok || len(importantMatched) != 0 {
		t.Fatalf("unexpected important matched payload: %#v", match["important_skills_matched"])
	}
	importantMentionedOnly, ok := match["important_skills_mentioned_only"].([]string)
	if !ok || len(importantMentionedOnly) != 0 {
		t.Fatalf("unexpected important mentioned-only payload: %#v", match["important_skills_mentioned_only"])
	}
	if match["seniority_assessment"] != "below" {
		t.Fatalf("unexpected seniority assessment: %#v", match["seniority_assessment"])
	}
}

func TestScoreExtractedDocumentRequiresExperienceProofForRequiredSkills(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc4",
		"title":       "CV Offre 18225",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18225",
		},
		"content": "/ COMPETENCES\nJava, Kotlin, Flutter, AWS\n/ FORMATIONS\nCertification AWS\n/ EXPERIENCES PROFESSIONNELLES\nDeveloppeur full stack Java Spring Boot\nRealisations: APIs REST, Angular, CI/CD",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 70, "justification": "Profil partiel.", "strengths": ["Spring Boot"], "gaps": ["Kotlin", "AWS"], "required_skills_matched": ["Kotlin", "Spring Boot", "AWS"], "required_skills_missing": [], "important_skills_matched": ["Dart"], "important_skills_missing": [], "optional_skills_matched": [], "seniority_assessment": "below"}`,
		},
		doc,
		"Mission full stack",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot", "AWS"},
			ImportantSkills: []string{"Dart"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requiredMatched, ok := match["required_skills_matched"].([]string)
	if !ok || len(requiredMatched) != 1 || requiredMatched[0] != "Spring Boot" {
		t.Fatalf("unexpected required matched payload: %#v", match["required_skills_matched"])
	}
	requiredMentionedOnly, ok := match["required_skills_mentioned_only"].([]string)
	if !ok || len(requiredMentionedOnly) != 2 {
		t.Fatalf("unexpected required mentioned-only payload: %#v", match["required_skills_mentioned_only"])
	}
	requiredMissing, ok := match["required_skills_missing"].([]string)
	if !ok || len(requiredMissing) != 0 {
		t.Fatalf("unexpected required missing payload: %#v", match["required_skills_missing"])
	}
	importantMatched, ok := match["important_skills_matched"].([]string)
	if !ok || len(importantMatched) != 0 {
		t.Fatalf("unexpected important matched payload: %#v", match["important_skills_matched"])
	}
	importantMentionedOnly, ok := match["important_skills_mentioned_only"].([]string)
	if !ok || len(importantMentionedOnly) != 1 || importantMentionedOnly[0] != "Dart" {
		t.Fatalf("unexpected important mentioned-only payload: %#v", match["important_skills_mentioned_only"])
	}
}

func TestScoreExtractedDocumentUsesContexteSectionAsExperienceProof(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc5",
		"title":       "CV Offre 18225",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "18225",
		},
		"content": "MBY\nCompetences techniques\nKotlin AWS Flutter\nFormations\nCertification AWS\nContexte\nRefonte microservices avec Spring Boot et Angular\nRealisations: APIs REST, CI/CD",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 65, "justification": "Profil partiel.", "strengths": ["Spring Boot"], "gaps": ["Kotlin", "AWS", "Dart"], "required_skills_matched": ["Kotlin", "Spring Boot", "AWS"], "required_skills_missing": [], "important_skills_matched": ["Dart"], "important_skills_missing": [], "optional_skills_matched": [], "seniority_assessment": "below"}`,
		},
		doc,
		"Mission full stack",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot", "AWS"},
			ImportantSkills: []string{"Dart"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requiredMatched, ok := match["required_skills_matched"].([]string)
	if !ok || len(requiredMatched) != 1 || requiredMatched[0] != "Spring Boot" {
		t.Fatalf("unexpected required matched payload: %#v", match["required_skills_matched"])
	}
	requiredMentionedOnly, ok := match["required_skills_mentioned_only"].([]string)
	if !ok || len(requiredMentionedOnly) != 2 {
		t.Fatalf("unexpected required mentioned-only payload: %#v", match["required_skills_mentioned_only"])
	}
	importantMatched, ok := match["important_skills_matched"].([]string)
	if !ok || len(importantMatched) != 0 {
		t.Fatalf("unexpected important matched payload: %#v", match["important_skills_matched"])
	}
	importantMentionedOnly, ok := match["important_skills_mentioned_only"].([]string)
	if !ok || len(importantMentionedOnly) != 1 || importantMentionedOnly[0] != "Dart" {
		t.Fatalf("unexpected important mentioned-only payload: %#v", match["important_skills_mentioned_only"])
	}
}

func TestAssessSenioritySupportsAnneesExpression(t *testing.T) {
	assessment := assessSeniority("7 années d'expérience en développement full stack Kotlin Spring Boot AWS", "Senior : 7-10 ans")
	if assessment != "match" {
		t.Fatalf("expected seniority match, got %q", assessment)
	}
}

func TestExtractExperienceProofTextPrefersExperienceSectionsAndDateBlocks(t *testing.T) {
	content := "Profil\nCompétences: Kotlin AWS Flutter\nExpériences clés\nSNCF CONNECT & TECH - Consultant Développeur Fullstack (Flutter - Kotlin - Spring Boot) - 03/2023 - 05/2025\nEnvironnement technique: Kotlin, Spring Boot, AWS\nContexte.\nAutre bloc"
	proof := extractExperienceProofText(content)
	if !strings.HasPrefix(proof, "Expériences clés") {
		t.Fatalf("unexpected proof text start: %q", proof[:minInt(len(proof), 80)])
	}

	content = "Intro\nDepuis Juillet 2024 – Développeur Java AWS Kotlin\nEnvironnement technique : Spring Boot, Kotlin, AWS\nAutre texte"
	proof = extractExperienceProofText(content)
	if !strings.Contains(proof, "Depuis Juillet 2024") {
		t.Fatalf("expected proof text to include date block, got %q", proof[:minInt(len(proof), 120)])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestScoreExtractedDocumentRewritesJustificationFromDeterministicCoverage(t *testing.T) {
	doc := map[string]interface{}{
		"document_id": "doc6",
		"title":       "CV Offre 19994",
		"doc_type":    "cv",
		"metadata": map[string]interface{}{
			"offre_id": "19994",
		},
		"content": "7 ans d'experience en Kotlin Spring Boot AWS et React.",
	}

	match, err := scoreExtractedDocument(
		context.Background(),
		&mockScoreLLM{
			answer: `{"score": 78, "justification": "Le candidat maitrise Dart.", "strengths": ["Kotlin"], "gaps": ["Aucun"], "required_skills_matched": ["Kotlin", "Spring Boot", "AWS"], "required_skills_missing": [], "important_skills_matched": ["Dart"], "important_skills_missing": [], "optional_skills_matched": [], "seniority_assessment": "match"}`,
		},
		doc,
		"Mission full stack",
		matchingRubric{
			RequiredSkills:  []string{"Kotlin", "Spring Boot", "AWS"},
			ImportantSkills: []string{"Dart"},
			OptionalSkills:  []string{"React"},
			SeniorityLabel:  "Senior : 7-10 ans",
		},
		600,
		1000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	justification, ok := match["justification"].(string)
	if !ok || strings.Contains(strings.ToLower(justification), "maitrise dart") {
		t.Fatalf("unexpected rewritten justification: %#v", match["justification"])
	}
	gaps, ok := match["gaps"].([]string)
	if !ok || len(gaps) == 0 || !strings.Contains(strings.ToLower(strings.Join(gaps, " ")), "dart") {
		t.Fatalf("unexpected rewritten gaps: %#v", match["gaps"])
	}
}
