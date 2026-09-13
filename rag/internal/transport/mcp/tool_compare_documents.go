package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var compareJSONPattern = regexp.MustCompile(`\{[\s\S]*"duplicate_likelihood"[\s\S]*\}`)
var compareOverallFieldPattern = regexp.MustCompile(`(?i)"overall_similarity"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
var compareSamePersonFieldPattern = regexp.MustCompile(`(?i)"same_person_likelihood"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
var compareDuplicateFieldPattern = regexp.MustCompile(`(?i)"duplicate_likelihood"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
var compareDecisionFieldPattern = regexp.MustCompile(`(?i)"decision_hint"\s*:\s*"(duplicate|related_but_distinct|distinct|uncertain)"`)

type CompareDocumentsTool struct {
	docRepo         *ExtractDocumentTextTool
	tenantRepo      *repository.TenantRepository
	llmClient       shared.LLMClient
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

type compareDeterministicMetrics struct {
	TextJaccard      float64  `json:"text_jaccard"`
	SharedTokenCount int      `json:"shared_token_count"`
	SharedTokens     []string `json:"shared_tokens"`
	ContentLengthA   int      `json:"content_length_a"`
	ContentLengthB   int      `json:"content_length_b"`
}

func NewCompareDocumentsTool(docRepo *repository.DocumentRepository, chunkRepo *repository.ChunkRepository, tenantRepo *repository.TenantRepository, llmClient shared.LLMClient, responseWrapper *ResponseWrapper, logger *zap.Logger) *CompareDocumentsTool {
	return &CompareDocumentsTool{
		docRepo:         NewExtractDocumentTextTool(docRepo, chunkRepo, tenantRepo, nil, logger),
		tenantRepo:      tenantRepo,
		llmClient:       llmClient,
		responseWrapper: responseWrapper,
		logger:          logger.Named("compare-documents-tool"),
	}
}

func (t *CompareDocumentsTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_compare_documents",
		Description: "Compare two full documents and estimate whether they represent the same candidate/profile",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required). If tenant_id is omitted, the default tenant of the deployment is used",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID for multi-tenant isolation",
				},
				"document_id_a": map[string]interface{}{
					"type":        "string",
					"description": "First document ID to compare",
				},
				"document_id_b": map[string]interface{}{
					"type":        "string",
					"description": "Second document ID to compare",
				},
				"max_text_chars": map[string]interface{}{
					"type":        "number",
					"description": "Maximum extracted text characters sent to the comparer per document (default: 12000)",
				},
				"max_tokens": map[string]interface{}{
					"type":        "number",
					"description": "Maximum tokens for the comparison response (default: 700)",
				},
			},
			Required: []string{"deployment_id", "document_id_a", "document_id_b"},
		},
	}
}

func (t *CompareDocumentsTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			DeploymentID string `json:"deployment_id"`
			TenantID     string `json:"tenant_id"`
			DocumentIDA  string `json:"document_id_a"`
			DocumentIDB  string `json:"document_id_b"`
			MaxTextChars int    `json:"max_text_chars"`
			MaxTokens    int    `json:"max_tokens"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}
		if strings.TrimSpace(args.DocumentIDA) == "" || strings.TrimSpace(args.DocumentIDB) == "" {
			return errorResult("document_id_a and document_id_b are required"), nil
		}
		if args.MaxTextChars <= 0 {
			args.MaxTextChars = 12000
		}
		if args.MaxTokens <= 0 {
			args.MaxTokens = 700
		}
		if t.llmClient == nil {
			return errorResult("llm client is not configured"), nil
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}

		documentsA, _, err := t.docRepo.extractByDocumentID(ctx, tenantID, args.DocumentIDA, nil, 500, false)
		if err != nil {
			return errorResult("failed to extract document A: " + err.Error()), nil
		}
		documentsB, _, err := t.docRepo.extractByDocumentID(ctx, tenantID, args.DocumentIDB, nil, 500, false)
		if err != nil {
			return errorResult("failed to extract document B: " + err.Error()), nil
		}
		if len(documentsA) == 0 || len(documentsB) == 0 {
			return errorResult("documents not found for comparison"), nil
		}

		result, err := compareExtractedDocuments(ctx, t.llmClient, documentsA[0], documentsB[0], args.MaxTokens, args.MaxTextChars)
		if err != nil {
			t.logger.Error("document comparison failed", zap.Error(err))
			return errorResult("document comparison failed: " + err.Error()), nil
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf(
				"Compared documents %s and %s (duplicate likelihood %.2f)",
				args.DocumentIDA,
				args.DocumentIDB,
				floatValue(result["duplicate_likelihood"]),
			)
			return t.responseWrapper.WrapToolResult(ctx, "rag_compare_documents", result, summary)
		}

		responseBytes, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: string(responseBytes),
				},
			},
		}, nil
	}
}

func compareExtractedDocuments(ctx context.Context, llmClient shared.LLMClient, docA, docB map[string]interface{}, maxTokens, maxTextChars int) (map[string]interface{}, error) {
	contentA := strings.TrimSpace(stringValue(docA["content"]))
	contentB := strings.TrimSpace(stringValue(docB["content"]))
	if contentA == "" || contentB == "" {
		return nil, fmt.Errorf("documents must both contain extracted text")
	}

	scoringA, truncatedA := trimForScoring(contentA, maxTextChars)
	scoringB, truncatedB := trimForScoring(contentB, maxTextChars)
	metrics := computeCompareDeterministicMetrics(contentA, contentB)
	prompt := buildCompareDocumentsPrompt(docA, docB, scoringA, scoringB, metrics)
	answer, err := llmClient.GenerateAnswer(ctx, prompt, maxTokens)
	if err != nil {
		return nil, err
	}

	overallSimilarity, samePersonLikelihood, duplicateLikelihood, decisionHint, sharedSignals, conflictingSignals, justification := parseCompareDocumentsAnswer(answer)
	if justification == "" {
		justification = buildFallbackCompareJustification(metrics, decisionHint)
	}

	return map[string]interface{}{
		"document_id_a":          stringValue(docA["document_id"]),
		"title_a":                stringValue(docA["title"]),
		"doc_type_a":             stringValue(docA["doc_type"]),
		"metadata_a":             mapValue(docA["metadata"]),
		"document_id_b":          stringValue(docB["document_id"]),
		"title_b":                stringValue(docB["title"]),
		"doc_type_b":             stringValue(docB["doc_type"]),
		"metadata_b":             mapValue(docB["metadata"]),
		"overall_similarity":     overallSimilarity,
		"same_person_likelihood": samePersonLikelihood,
		"duplicate_likelihood":   duplicateLikelihood,
		"decision_hint":          decisionHint,
		"shared_signals":         sharedSignals,
		"conflicting_signals":    conflictingSignals,
		"justification":          justification,
		"deterministic":          metrics,
		"content_length_a":       len(contentA),
		"content_length_b":       len(contentB),
		"text_truncated_a":       truncatedA,
		"text_truncated_b":       truncatedB,
		"raw_answer":             truncateScoreDocumentString(answer, 1200),
	}, nil
}

func buildCompareDocumentsPrompt(docA, docB map[string]interface{}, contentA, contentB string, metrics compareDeterministicMetrics) string {
	return "Tu compares deux CV complets pour determiner s'ils representent probablement la meme personne.\n" +
		"Ta mission n'est PAS de dire si les profils se ressemblent techniquement, mais si ce sont probablement le MEME candidat.\n" +
		"Utilise les indices suivants:\n" +
		"- signaux forts positifs: memes experiences, memes employeurs aux memes periodes, memes missions, memes technologies dans les memes contextes, memes coordonnees, meme nom/prenom, chronologie tres proche\n" +
		"- signaux forts negatifs: noms differents, chronologies incompatibles, employeurs differents, parcours differents, specialisations differentes, simples templates ESN communs\n" +
		"- si les CV partagent seulement un stack similaire mais racontent des parcours differents, ce n'est PAS un doublon\n" +
		"Retourne UNIQUEMENT un JSON strict au format:\n" +
		"{\"overall_similarity\": <0-1>, \"same_person_likelihood\": <0-1>, \"duplicate_likelihood\": <0-1>, \"decision_hint\": \"duplicate|related_but_distinct|distinct|uncertain\", \"shared_signals\": [\"...\"], \"conflicting_signals\": [\"...\"], \"justification\": \"<francais>\"}\n\n" +
		"=== SIGNAUX DETERMINISTES ===\n" +
		fmt.Sprintf("Text jaccard: %.3f\n", metrics.TextJaccard) +
		fmt.Sprintf("Tokens partages: %d\n", metrics.SharedTokenCount) +
		"Exemples de tokens partages: " + strings.Join(metrics.SharedTokens, ", ") + "\n\n" +
		"=== DOCUMENT A ===\n" +
		"Titre: " + strings.TrimSpace(stringValue(docA["title"])) + "\n" +
		"Type: " + strings.TrimSpace(stringValue(docA["doc_type"])) + "\n" +
		"Texte:\n" + strings.TrimSpace(contentA) + "\n\n" +
		"=== DOCUMENT B ===\n" +
		"Titre: " + strings.TrimSpace(stringValue(docB["title"])) + "\n" +
		"Type: " + strings.TrimSpace(stringValue(docB["doc_type"])) + "\n" +
		"Texte:\n" + strings.TrimSpace(contentB)
}

func parseCompareDocumentsAnswer(answer string) (float64, float64, float64, string, []string, []string, string) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return 0, 0, 0, "uncertain", nil, nil, ""
	}

	type payload struct {
		OverallSimilarity    float64  `json:"overall_similarity"`
		SamePersonLikelihood float64  `json:"same_person_likelihood"`
		DuplicateLikelihood  float64  `json:"duplicate_likelihood"`
		DecisionHint         string   `json:"decision_hint"`
		SharedSignals        []string `json:"shared_signals"`
		ConflictingSignals   []string `json:"conflicting_signals"`
		Justification        string   `json:"justification"`
	}

	candidates := make([]string, 0, 4)
	candidates = append(candidates, answer)
	if matches := fencedJSONPattern.FindAllStringSubmatch(answer, -1); len(matches) > 0 {
		for _, match := range matches {
			if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
				candidates = append(candidates, strings.TrimSpace(match[1]))
			}
		}
	}
	if match := compareJSONPattern.FindString(answer); match != "" {
		candidates = append(candidates, strings.TrimSpace(match))
	}

	for _, candidate := range candidates {
		var parsed payload
		if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
			continue
		}
		return normalizeLikelihood(parsed.OverallSimilarity),
			normalizeLikelihood(parsed.SamePersonLikelihood),
			normalizeLikelihood(parsed.DuplicateLikelihood),
			normalizeDecisionHint(parsed.DecisionHint),
			sanitizeStringList(parsed.SharedSignals),
			sanitizeStringList(parsed.ConflictingSignals),
			strings.TrimSpace(parsed.Justification)
	}

	overall := findCompareFloat(answer, compareOverallFieldPattern)
	samePerson := findCompareFloat(answer, compareSamePersonFieldPattern)
	duplicate := findCompareFloat(answer, compareDuplicateFieldPattern)
	decisionHint := "uncertain"
	if match := compareDecisionFieldPattern.FindStringSubmatch(answer); len(match) == 2 {
		decisionHint = normalizeDecisionHint(match[1])
	}
	return overall, samePerson, duplicate, decisionHint, nil, nil, truncateScoreDocumentString(answer, 400)
}

func computeCompareDeterministicMetrics(contentA, contentB string) compareDeterministicMetrics {
	tokensA := tokenizeCompareText(contentA)
	tokensB := tokenizeCompareText(contentB)
	shared := make([]string, 0)
	for token := range tokensA {
		if tokensB[token] {
			shared = append(shared, token)
		}
	}
	sort.Strings(shared)
	if len(shared) > 12 {
		shared = shared[:12]
	}

	return compareDeterministicMetrics{
		TextJaccard:      compareTokenJaccard(tokensA, tokensB),
		SharedTokenCount: len(shared),
		SharedTokens:     shared,
		ContentLengthA:   len(contentA),
		ContentLengthB:   len(contentB),
	}
}

func tokenizeCompareText(content string) map[string]bool {
	normalized := strings.ToLower(content)
	replacer := strings.NewReplacer(
		"\n", " ",
		"\r", " ",
		"\t", " ",
		".", " ",
		",", " ",
		";", " ",
		":", " ",
		"/", " ",
		"\\", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
		"-", " ",
		"_", " ",
		"|", " ",
		"'", " ",
		"\"", " ",
	)
	normalized = replacer.Replace(normalized)
	stopwords := map[string]struct{}{
		"avec": {}, "pour": {}, "dans": {}, "sur": {}, "les": {}, "des": {}, "une": {}, "the": {}, "and": {},
		"ingénieur": {}, "ingenieur": {}, "developpeur": {}, "developer": {}, "consultant": {}, "profil": {},
		"experience": {}, "experiences": {}, "compétences": {}, "competences": {}, "skills": {}, "senior": {},
		"full": {}, "stack": {}, "backend": {}, "frontend": {}, "projet": {}, "mission": {}, "client": {},
		"java": {}, "react": {}, "spring": {}, "kotlin": {}, "aws": {},
	}
	out := make(map[string]bool)
	for _, token := range strings.Fields(normalized) {
		if len(token) < 4 {
			continue
		}
		if _, ignored := stopwords[token]; ignored {
			continue
		}
		out[token] = true
	}
	return out
}

func compareTokenJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for token := range a {
		if b[token] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func normalizeLikelihood(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1 && value <= 100:
		return value / 100
	case value > 1:
		return 1
	default:
		return value
	}
}

func normalizeDecisionHint(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "duplicate", "related_but_distinct", "distinct", "uncertain":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "uncertain"
	}
}

func findCompareFloat(value string, pattern *regexp.Regexp) float64 {
	match := pattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0
	}
	var parsed float64
	if _, err := fmt.Sscanf(match[1], "%f", &parsed); err != nil {
		return 0
	}
	return normalizeLikelihood(parsed)
}

func buildFallbackCompareJustification(metrics compareDeterministicMetrics, decisionHint string) string {
	switch decisionHint {
	case "duplicate":
		return fmt.Sprintf("Les deux documents paraissent tres proches avec une similarite textuelle %.2f.", metrics.TextJaccard)
	case "distinct":
		return fmt.Sprintf("Les deux documents partagent peu de signaux identitaires forts (similarite textuelle %.2f).", metrics.TextJaccard)
	case "related_but_distinct":
		return fmt.Sprintf("Les deux documents semblent proches techniquement mais pas assez pour conclure au meme candidat (similarite textuelle %.2f).", metrics.TextJaccard)
	default:
		return fmt.Sprintf("Comparaison inconclusive, similarite textuelle %.2f.", metrics.TextJaccard)
	}
}

func floatValue(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}
