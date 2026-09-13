package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

var scoreJSONPattern = regexp.MustCompile(`\{[\s\S]*"score"[\s\S]*\}`)
var fencedJSONPattern = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{[\\s\\S]*?\\})\\s*```")
var scoreFieldPattern = regexp.MustCompile(`(?i)"score"\s*:\s*(\d{1,3})`)
var justificationFieldPattern = regexp.MustCompile(`(?is)"justification"\s*:\s*"((?:\\.|[^"\\])*)"`)
var seniorityFieldPattern = regexp.MustCompile(`(?i)"seniority_assessment"\s*:\s*"(below|match|above|unknown)"`)
var seniorityRangePattern = regexp.MustCompile(`(?i)(\d{1,2})\s*-\s*(\d{1,2})\s*ans`)
var experienceYearsPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\d{1,2})\s*\+?\s*ans?\s+d['’]exp[ée]rience`),
	regexp.MustCompile(`(?i)(\d{1,2})\s*\+?\s*ann?[ée]es?\s+d['’]exp[ée]rience`),
	regexp.MustCompile(`(?i)plus de\s+(\d{1,2})\s+ans`),
	regexp.MustCompile(`(?i)plus de\s+(\d{1,2})\s+ann?[ée]es?`),
	regexp.MustCompile(`(?i)(\d{1,2})\s+ans?\s+exp[ée]rience`),
	regexp.MustCompile(`(?i)(\d{1,2})\s+ann?[ée]es?\s+d['’]exp[ée]rience`),
	regexp.MustCompile(`(?i)\b(\d{1,2})\s+ans\b`),
	regexp.MustCompile(`(?i)\b(\d{1,2})\s+ann?[ée]es?\b`),
}
var experienceSectionStartPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^\s*/\s*exp[ée]riences?\s+professionnelles?\s*$`),
	regexp.MustCompile(`(?im)^\s*exp[ée]riences?\s+cl[ée]s\s*$`),
	regexp.MustCompile(`(?im)^\s*exp[ée]riences?\s+principales?\s*$`),
	regexp.MustCompile(`(?im)^\s*exp[ée]riences?\s+professionnelles?\s*$`),
	regexp.MustCompile(`(?im)^\s*r[ée]f[ée]rences?\s+significatives?\s*$`),
	regexp.MustCompile(`(?im)^\s*contexte\b.*$`),
}
var experienceEntryStartPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^[^\n]{0,120}(?:-|–|—)[^\n]{0,120}(?:de\s+)?(?:19|20)\d{2}[^\n]{0,80}$`),
	regexp.MustCompile(`(?im)^[^\n]{0,120}(?:-|–|—)[^\n]{0,120}(?:janvier|f[ée]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[ée]cembre)[^\n]{0,80}$`),
	regexp.MustCompile(`(?im)^\s*depuis\s+(?:janvier|f[ée]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[ée]cembre|\d{1,2}/\d{4}|\d{4})[^\n]*$`),
	regexp.MustCompile(`(?im)^\s*de\s+(?:janvier|f[ée]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[ée]cembre|\d{1,2}/\d{4}|\d{4})[^\n]*$`),
}

type ScoreDocumentTool struct {
	docRepo         *repository.DocumentRepository
	chunkRepo       *repository.ChunkRepository
	tenantRepo      *repository.TenantRepository
	llmClient       shared.LLMClient
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

type matchingRubric struct {
	RequiredSkills  []string
	ImportantSkills []string
	OptionalSkills  []string
	SeniorityLabel  string
}

type skillCoverage struct {
	Proved        []string
	MentionedOnly []string
	Missing       []string
}

func NewScoreDocumentTool(docRepo *repository.DocumentRepository, chunkRepo *repository.ChunkRepository, tenantRepo *repository.TenantRepository, llmClient shared.LLMClient, responseWrapper *ResponseWrapper, logger *zap.Logger) *ScoreDocumentTool {
	return &ScoreDocumentTool{
		docRepo:         docRepo,
		chunkRepo:       chunkRepo,
		tenantRepo:      tenantRepo,
		llmClient:       llmClient,
		responseWrapper: responseWrapper,
		logger:          logger.Named("score-document-tool"),
	}
}

func (t *ScoreDocumentTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_score_document",
		Description: "Score one or more documents against a mission/requirement text using the full extracted document text",
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
				"document_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional document ID to score directly",
				},
				"metadata_filters": map[string]interface{}{
					"type":        "object",
					"description": "Optional metadata filters to select matching documents. Example: {\"offre_id\": \"18224\"}",
					"additionalProperties": map[string]interface{}{
						"type": "string",
					},
				},
				"requirement_text": map[string]interface{}{
					"type":        "string",
					"description": "Mission or requirement text used as the scoring reference",
				},
				"max_documents": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of documents to score when using metadata filters (default: 10)",
				},
				"max_chunks": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of chunks to inspect across all documents (default: 500)",
				},
				"max_text_chars": map[string]interface{}{
					"type":        "number",
					"description": "Maximum extracted text characters sent to the scorer per document (default: 20000)",
				},
				"max_tokens": map[string]interface{}{
					"type":        "number",
					"description": "Maximum tokens for the scoring response (default: 800)",
				},
				"required_skills": map[string]interface{}{
					"type":        "array",
					"description": "Skills that are mandatory for the mission",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"important_skills": map[string]interface{}{
					"type":        "array",
					"description": "Skills that are important but not strictly mandatory",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"optional_skills": map[string]interface{}{
					"type":        "array",
					"description": "Nice-to-have skills",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"seniority_label": map[string]interface{}{
					"type":        "string",
					"description": "Requested seniority label (for example Senior : 7-10 ans)",
				},
			},
			Required: []string{"deployment_id", "requirement_text"},
		},
	}
}

func (t *ScoreDocumentTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			DeploymentID    string            `json:"deployment_id"`
			TenantID        string            `json:"tenant_id"`
			DocumentID      string            `json:"document_id"`
			MetadataFilter  map[string]string `json:"metadata_filters"`
			Requirement     string            `json:"requirement_text"`
			MaxDocuments    int               `json:"max_documents"`
			MaxChunks       int               `json:"max_chunks"`
			MaxTextChars    int               `json:"max_text_chars"`
			MaxTokens       int               `json:"max_tokens"`
			RequiredSkills  []string          `json:"required_skills"`
			ImportantSkills []string          `json:"important_skills"`
			OptionalSkills  []string          `json:"optional_skills"`
			SeniorityLabel  string            `json:"seniority_label"`
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
		if strings.TrimSpace(args.Requirement) == "" {
			return errorResult("requirement_text is required"), nil
		}
		if args.DocumentID == "" && len(args.MetadataFilter) == 0 {
			return errorResult("document_id or metadata_filters is required"), nil
		}
		if args.MaxDocuments <= 0 {
			args.MaxDocuments = 10
		}
		if args.MaxChunks <= 0 {
			args.MaxChunks = 500
		}
		if args.MaxTextChars <= 0 {
			args.MaxTextChars = 20000
		}
		if args.MaxTokens <= 0 {
			args.MaxTokens = 800
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}
		if t.llmClient == nil {
			return errorResult("llm client is not configured"), nil
		}

		var documents []map[string]interface{}
		var totalChunks int
		if args.DocumentID != "" {
			documents, totalChunks, err = t.extractByDocumentID(ctx, tenantID, args.DocumentID, args.MetadataFilter, args.MaxChunks)
		} else {
			documents, totalChunks, err = t.extractByMetadata(ctx, tenantID, args.MetadataFilter, args.MaxDocuments, args.MaxChunks)
		}
		if err != nil {
			t.logger.Error("document scoring extraction failed", zap.Error(err))
			return errorResult("document scoring extraction failed: " + err.Error()), nil
		}

		matches := make([]map[string]interface{}, 0, len(documents))
		rubric := matchingRubric{
			RequiredSkills:  sanitizeStringList(args.RequiredSkills),
			ImportantSkills: sanitizeStringList(args.ImportantSkills),
			OptionalSkills:  sanitizeStringList(args.OptionalSkills),
			SeniorityLabel:  strings.TrimSpace(args.SeniorityLabel),
		}
		for _, doc := range documents {
			match, err := scoreExtractedDocument(ctx, t.llmClient, doc, args.Requirement, rubric, args.MaxTokens, args.MaxTextChars)
			if err != nil {
				t.logger.Warn("document scoring failed",
					zap.String("document_id", stringValue(doc["document_id"])),
					zap.Error(err))
				match = map[string]interface{}{
					"document_id": stringValue(doc["document_id"]),
					"title":       stringValue(doc["title"]),
					"doc_type":    stringValue(doc["doc_type"]),
					"metadata":    mapValue(doc["metadata"]),
					"score":       0,
					"error":       err.Error(),
				}
			}
			matches = append(matches, match)
		}

		sort.Slice(matches, func(i, j int) bool {
			return intValue(matches[i]["score"]) > intValue(matches[j]["score"])
		})

		response := map[string]interface{}{
			"requirement_text": args.Requirement,
			"document_count":   len(matches),
			"chunk_count":      totalChunks,
			"matches":          matches,
		}
		if len(matches) > 0 {
			for _, key := range []string{"document_id", "title", "doc_type", "metadata", "score", "justification", "strengths", "gaps", "text_truncated", "content_length"} {
				if value, ok := matches[0][key]; ok {
					response[key] = value
				}
			}
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("Scored %d document(s) against requirement", len(matches))
			return t.responseWrapper.WrapToolResult(ctx, "rag_score_document", response, summary)
		}

		responseBytes, _ := json.Marshal(response)
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

func (t *ScoreDocumentTool) extractByDocumentID(ctx context.Context, tenantID xid.ID, documentID string, metadataFilters map[string]string, maxChunks int) ([]map[string]interface{}, int, error) {
	docID, err := xid.FromString(documentID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid document_id")
	}
	doc, err := t.docRepo.Get(ctx, docID)
	if err != nil {
		return nil, 0, err
	}
	if doc.TenantID != tenantID {
		return nil, 0, fmt.Errorf("document does not belong to tenant")
	}

	chunks, err := t.chunkRepo.ListByDocument(ctx, docID)
	if err != nil {
		return nil, 0, err
	}

	filtered := make([]*repositoryChunkView, 0, len(chunks))
	for _, ch := range chunks {
		if len(metadataFilters) > 0 && !metadataMatchesFilters(ch.Metadata, metadataFilters) && !metadataMatchesFilters(doc.Metadata, metadataFilters) {
			continue
		}
		filtered = append(filtered, &repositoryChunkView{
			ID:       ch.ID.String(),
			Text:     ch.Text,
			Metadata: ch.Metadata,
		})
		if maxChunks > 0 && len(filtered) >= maxChunks {
			break
		}
	}

	if len(metadataFilters) > 0 && len(filtered) == 0 {
		return []map[string]interface{}{}, 0, nil
	}

	payload := buildExtractedDocumentPayload(doc.ID.String(), doc.Title, doc.DocType, doc.Metadata, filtered, false)
	return []map[string]interface{}{payload}, len(filtered), nil
}

func (t *ScoreDocumentTool) extractByMetadata(ctx context.Context, tenantID xid.ID, metadataFilters map[string]string, maxDocuments, maxChunks int) ([]map[string]interface{}, int, error) {
	const pageSize = 500

	type docAccumulator struct {
		documentID string
		doc        map[string]interface{}
		chunks     []*repositoryChunkView
	}

	docAccumulators := make(map[string]*docAccumulator)
	docOrder := make([]string, 0)
	totalChunks := 0
	docCache := make(map[string]map[string]interface{})

	loadDoc := func(documentID xid.ID) (map[string]interface{}, error) {
		key := documentID.String()
		if cached, ok := docCache[key]; ok {
			return cached, nil
		}
		doc, err := t.docRepo.Get(ctx, documentID)
		if err != nil {
			return nil, err
		}
		if doc.TenantID != tenantID {
			return nil, fmt.Errorf("document %s does not belong to tenant", key)
		}
		payload := map[string]interface{}{
			"document_id": key,
			"title":       doc.Title,
			"doc_type":    doc.DocType,
			"metadata":    doc.Metadata,
		}
		docCache[key] = payload
		return payload, nil
	}

	for offset := 0; ; offset += pageSize {
		page, err := t.chunkRepo.ListByTenant(ctx, tenantID, offset, pageSize)
		if err != nil {
			return nil, 0, err
		}
		if len(page) == 0 {
			break
		}

		for _, ch := range page {
			if !metadataMatchesFilters(ch.Metadata, metadataFilters) {
				continue
			}

			key := ch.DocumentID.String()
			acc, ok := docAccumulators[key]
			if !ok {
				if maxDocuments > 0 && len(docOrder) >= maxDocuments {
					continue
				}
				docPayload, err := loadDoc(ch.DocumentID)
				if err != nil {
					return nil, 0, err
				}
				acc = &docAccumulator{
					documentID: key,
					doc:        docPayload,
					chunks:     make([]*repositoryChunkView, 0),
				}
				docAccumulators[key] = acc
				docOrder = append(docOrder, key)
			}

			if maxChunks > 0 && totalChunks >= maxChunks {
				break
			}

			acc.chunks = append(acc.chunks, &repositoryChunkView{
				ID:       ch.ID.String(),
				Text:     ch.Text,
				Metadata: ch.Metadata,
			})
			totalChunks++
		}

		if maxChunks > 0 && totalChunks >= maxChunks {
			break
		}
	}

	documents := make([]map[string]interface{}, 0, len(docOrder))
	for _, key := range docOrder {
		acc := docAccumulators[key]
		if acc == nil || len(acc.chunks) == 0 {
			continue
		}
		documents = append(documents, buildExtractedDocumentPayload(
			acc.documentID,
			stringValue(acc.doc["title"]),
			stringValue(acc.doc["doc_type"]),
			mapValue(acc.doc["metadata"]),
			acc.chunks,
			false,
		))
	}

	return documents, totalChunks, nil
}

func scoreExtractedDocument(ctx context.Context, llmClient shared.LLMClient, doc map[string]interface{}, requirementText string, rubric matchingRubric, maxTokens, maxTextChars int) (map[string]interface{}, error) {
	content := strings.TrimSpace(stringValue(doc["content"]))
	if content == "" {
		return nil, fmt.Errorf("document has no extracted text")
	}

	scoringText, truncated := trimForScoring(content, maxTextChars)
	prompt := buildScoreDocumentPrompt(requirementText, rubric, stringValue(doc["title"]), stringValue(doc["doc_type"]), scoringText)
	answer, err := llmClient.GenerateAnswer(ctx, prompt, maxTokens)
	if err != nil {
		return nil, err
	}

	score, justification, strengths, gaps, _, _, _, _, _, seniorityAssessment := parseScoreDocumentAnswer(answer)
	requiredCoverage := classifyRequiredSkillCoverage(content, rubric.RequiredSkills)
	importantCoverage := classifyMentionVsProofCoverage(content, rubric.ImportantSkills)
	optionalCoverage := classifyMentionVsProofCoverage(content, rubric.OptionalSkills)
	optionalMatched := append(append([]string{}, optionalCoverage.Proved...), optionalCoverage.MentionedOnly...)
	if deterministicSeniority := assessSeniority(content, rubric.SeniorityLabel); deterministicSeniority != "unknown" {
		seniorityAssessment = deterministicSeniority
	}
	justification = buildDeterministicJustification(score, requiredCoverage, importantCoverage, seniorityAssessment)
	gaps = buildDeterministicGaps(requiredCoverage, importantCoverage, seniorityAssessment)
	return map[string]interface{}{
		"document_id":                     stringValue(doc["document_id"]),
		"title":                           stringValue(doc["title"]),
		"doc_type":                        stringValue(doc["doc_type"]),
		"metadata":                        mapValue(doc["metadata"]),
		"score":                           score,
		"justification":                   justification,
		"strengths":                       strengths,
		"gaps":                            gaps,
		"required_skills_matched":         requiredCoverage.Proved,
		"required_skills_mentioned_only":  requiredCoverage.MentionedOnly,
		"required_skills_missing":         requiredCoverage.Missing,
		"important_skills_matched":        importantCoverage.Proved,
		"important_skills_mentioned_only": importantCoverage.MentionedOnly,
		"important_skills_missing":        importantCoverage.Missing,
		"optional_skills_matched":         optionalMatched,
		"seniority_assessment":            seniorityAssessment,
		"content_length":                  len(content),
		"text_truncated":                  truncated,
		"raw_answer":                      truncateScoreDocumentString(answer, 800),
	}, nil
}

func buildDeterministicJustification(score int, requiredCoverage, importantCoverage skillCoverage, seniorityAssessment string) string {
	parts := []string{fitLabelForScore(score)}

	switch {
	case len(requiredCoverage.Missing) == 0 && len(requiredCoverage.MentionedOnly) == 0:
		parts = append(parts, "Toutes les competences obligatoires sont prouvees par des experiences concretes.")
	case len(requiredCoverage.Missing) == 0:
		parts = append(parts, "Les competences obligatoires sont couvertes, mais certaines restent seulement mentionnees et pas clairement prouvees: "+joinList(requiredCoverage.MentionedOnly)+".")
	default:
		parts = append(parts, "Des competences obligatoires restent manquantes ou non prouvees: "+joinList(append(append([]string{}, requiredCoverage.Missing...), requiredCoverage.MentionedOnly...))+".")
	}

	switch {
	case len(importantCoverage.Proved) > 0:
		parts = append(parts, "Les competences importantes prouvees incluent "+joinList(importantCoverage.Proved)+".")
	case len(importantCoverage.MentionedOnly) > 0:
		parts = append(parts, "Les competences importantes sont mentionnees mais pas clairement prouvees: "+joinList(importantCoverage.MentionedOnly)+".")
	case len(importantCoverage.Missing) > 0:
		parts = append(parts, "Les competences importantes absentes incluent "+joinList(importantCoverage.Missing)+".")
	}

	switch seniorityAssessment {
	case "below":
		parts = append(parts, "La seniorite observee reste en dessous du niveau demande.")
	case "match":
		parts = append(parts, "La seniorite observee correspond au niveau demande.")
	case "above":
		parts = append(parts, "La seniorite observee est au-dessus du niveau demande.")
	}

	return strings.Join(parts, " ")
}

func buildDeterministicGaps(requiredCoverage, importantCoverage skillCoverage, seniorityAssessment string) []string {
	gaps := make([]string, 0, len(requiredCoverage.Missing)+len(requiredCoverage.MentionedOnly)+len(importantCoverage.Missing)+len(importantCoverage.MentionedOnly)+1)
	for _, skill := range requiredCoverage.Missing {
		gaps = append(gaps, fmt.Sprintf("Absence de %s (competence obligatoire)", skill))
	}
	for _, skill := range requiredCoverage.MentionedOnly {
		gaps = append(gaps, fmt.Sprintf("%s seulement mentionne, sans preuve d'experience claire", skill))
	}
	for _, skill := range importantCoverage.Missing {
		gaps = append(gaps, fmt.Sprintf("Absence de %s (competence importante)", skill))
	}
	for _, skill := range importantCoverage.MentionedOnly {
		gaps = append(gaps, fmt.Sprintf("%s mentionne mais non prouve en experience", skill))
	}
	if seniorityAssessment == "below" {
		gaps = append(gaps, "Seniorite en dessous du niveau demande")
	}
	if len(gaps) == 0 {
		gaps = append(gaps, "Aucun gap majeur identifie")
	}
	return gaps
}

func fitLabelForScore(score int) string {
	switch {
	case score >= 90:
		return "Le profil presente un excellent fit pour la mission."
	case score >= 80:
		return "Le profil presente un tres bon fit pour la mission."
	case score >= 65:
		return "Le profil presente un bon fit partiel pour la mission."
	case score >= 45:
		return "Le profil presente un fit faible pour la mission."
	default:
		return "Le profil presente un mauvais fit pour la mission."
	}
}

func joinList(values []string) string {
	return strings.Join(sanitizeStringList(values), ", ")
}

func buildScoreDocumentPrompt(requirementText string, rubric matchingRubric, title, docType, content string) string {
	requiredLine := "(non fourni)"
	if len(rubric.RequiredSkills) > 0 {
		requiredLine = strings.Join(rubric.RequiredSkills, ", ")
	}
	importantLine := "(non fourni)"
	if len(rubric.ImportantSkills) > 0 {
		importantLine = strings.Join(rubric.ImportantSkills, ", ")
	}
	optionalLine := "(non fourni)"
	if len(rubric.OptionalSkills) > 0 {
		optionalLine = strings.Join(rubric.OptionalSkills, ", ")
	}
	seniorityLine := rubric.SeniorityLabel
	if seniorityLine == "" {
		seniorityLine = "(non fourni)"
	}

	return "Tu es un expert en recrutement IT. Tu dois evaluer UN CV par rapport a UNE mission.\n" +
		"Analyse uniquement la correspondance competences/experience/exigences.\n" +
		"Ignore les coordonnees, emails commerciaux et elements non pertinents pour le matching.\n" +
		"Utilise IMPERATIVEMENT la grille suivante pour evaluer l'adequation:\n" +
		"- Les competences OBLIGATOIRES sont eliminatoires: si une competence obligatoire manque ou n'est pas prouvee, le score doit etre fortement penalise.\n" +
		"- Si 2 competences obligatoires ou plus manquent, le score final ne doit pas depasser 55.\n" +
		"- Si 1 competence obligatoire manque, le score final ne doit pas depasser 72.\n" +
		"- Si la seniorite demandee n'est pas atteinte, le score final ne doit pas depasser 70.\n" +
		"- Si plusieurs competences importantes manquent, le score final ne doit pas depasser 82.\n" +
		"- Utilise toute l'echelle: 90-100 excellent fit, 80-89 bon fit, 65-79 fit partiel, 45-64 fit faible, 0-44 mauvais fit.\n" +
		"- Ne donne PAS un score eleve a un profil generaliste si les competences obligatoires ne sont pas explicitement presentes.\n" +
		"Retourne UNIQUEMENT un JSON strict au format:\n" +
		"{\"score\": <0-100>, \"justification\": \"<francais>\", \"strengths\": [\"...\"], \"gaps\": [\"...\"], \"required_skills_matched\": [\"...\"], \"required_skills_missing\": [\"...\"], \"important_skills_matched\": [\"...\"], \"important_skills_missing\": [\"...\"], \"optional_skills_matched\": [\"...\"], \"seniority_assessment\": \"below|match|above|unknown\"}\n" +
		"Le score doit etre defensible et refleter l'adequation reelle du profil.\n\n" +
		"=== MISSION / EXIGENCES ===\n" + strings.TrimSpace(requirementText) + "\n\n" +
		"=== GRILLE D'EVALUATION ===\n" +
		"Seniorite demandee: " + seniorityLine + "\n" +
		"Competences obligatoires: " + requiredLine + "\n" +
		"Competences importantes: " + importantLine + "\n" +
		"Competences appreciees: " + optionalLine + "\n\n" +
		"=== DOCUMENT ===\n" +
		"Titre: " + strings.TrimSpace(title) + "\n" +
		"Type: " + strings.TrimSpace(docType) + "\n\n" +
		"=== CV / TEXTE EXTRAIT ===\n" + strings.TrimSpace(content)
}

func parseScoreDocumentAnswer(answer string) (int, string, []string, []string, []string, []string, []string, []string, []string, string) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return 0, "", nil, nil, nil, nil, nil, nil, nil, "unknown"
	}

	type payload struct {
		Score               float64  `json:"score"`
		Justification       string   `json:"justification"`
		Strengths           []string `json:"strengths"`
		Gaps                []string `json:"gaps"`
		RequiredMatched     []string `json:"required_skills_matched"`
		RequiredMissing     []string `json:"required_skills_missing"`
		ImportantMatched    []string `json:"important_skills_matched"`
		ImportantMissing    []string `json:"important_skills_missing"`
		OptionalMatched     []string `json:"optional_skills_matched"`
		SeniorityAssessment string   `json:"seniority_assessment"`
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
	if match := scoreJSONPattern.FindString(answer); match != "" {
		candidates = append(candidates, strings.TrimSpace(match))
	}

	for _, candidate := range candidates {
		var parsed payload
		if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
			continue
		}
		if int(parsed.Score) == 0 && strings.Contains(parsed.Justification, "\"score\"") {
			nestedScore, nestedJustification, nestedStrengths, nestedGaps, nestedRequiredMatched, nestedRequiredMissing, nestedImportantMatched, nestedImportantMissing, nestedOptionalMatched, nestedSeniority := parseScoreDocumentAnswer(parsed.Justification)
			if nestedScore > 0 || nestedJustification != "" {
				return nestedScore, nestedJustification, nestedStrengths, nestedGaps, nestedRequiredMatched, nestedRequiredMissing, nestedImportantMatched, nestedImportantMissing, nestedOptionalMatched, nestedSeniority
			}
		}

		score := int(parsed.Score)
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}

		seniorityAssessment := strings.ToLower(strings.TrimSpace(parsed.SeniorityAssessment))
		switch seniorityAssessment {
		case "below", "match", "above", "unknown":
		default:
			seniorityAssessment = "unknown"
		}

		return score,
			strings.TrimSpace(parsed.Justification),
			sanitizeStringList(parsed.Strengths),
			sanitizeStringList(parsed.Gaps),
			sanitizeStringList(parsed.RequiredMatched),
			sanitizeStringList(parsed.RequiredMissing),
			sanitizeStringList(parsed.ImportantMatched),
			sanitizeStringList(parsed.ImportantMissing),
			sanitizeStringList(parsed.OptionalMatched),
			seniorityAssessment
	}

	for _, candidate := range candidates {
		score, justification, seniorityAssessment, ok := parseScoreDocumentAnswerWithRegex(candidate)
		if !ok {
			continue
		}
		return score, justification, nil, nil, nil, nil, nil, nil, nil, seniorityAssessment
	}

	return 0, truncateScoreDocumentString(answer, 400), nil, nil, nil, nil, nil, nil, nil, "unknown"
}

func trimForScoring(content string, maxTextChars int) (string, bool) {
	if maxTextChars <= 0 || len(content) <= maxTextChars {
		return content, false
	}
	return content[:maxTextChars], true
}

func sanitizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func reconcileSkillCoverage(content string, rubricSkills []string) ([]string, []string) {
	matched := make([]string, 0, len(rubricSkills))
	missing := make([]string, 0, len(rubricSkills))
	for _, skill := range sanitizeStringList(rubricSkills) {
		if skillCovered(content, skill) {
			matched = append(matched, skill)
		} else {
			missing = append(missing, skill)
		}
	}
	return matched, missing
}

func classifyRequiredSkillCoverage(content string, rubricSkills []string) skillCoverage {
	proofText := extractExperienceProofText(content)
	if strings.TrimSpace(proofText) == "" {
		proofText = content
	}
	return classifySkillCoverage(proofText, content, rubricSkills)
}

func classifyMentionVsProofCoverage(content string, rubricSkills []string) skillCoverage {
	proofText := extractExperienceProofText(content)
	if strings.TrimSpace(proofText) == "" {
		proofText = content
	}
	return classifySkillCoverage(proofText, content, rubricSkills)
}

func classifySkillCoverage(proofText, fullText string, rubricSkills []string) skillCoverage {
	coverage := skillCoverage{
		Proved:        make([]string, 0, len(rubricSkills)),
		MentionedOnly: make([]string, 0, len(rubricSkills)),
		Missing:       make([]string, 0, len(rubricSkills)),
	}
	for _, skill := range sanitizeStringList(rubricSkills) {
		switch {
		case skillCovered(proofText, skill):
			coverage.Proved = append(coverage.Proved, skill)
		case skillCovered(fullText, skill):
			coverage.MentionedOnly = append(coverage.MentionedOnly, skill)
		default:
			coverage.Missing = append(coverage.Missing, skill)
		}
	}
	return coverage
}

func extractExperienceProofText(content string) string {
	bestStart := -1
	for _, pattern := range experienceSectionStartPatterns {
		loc := pattern.FindStringIndex(content)
		if loc == nil {
			continue
		}
		if bestStart == -1 || loc[0] < bestStart {
			bestStart = loc[0]
		}
	}
	if bestStart >= 0 {
		return strings.TrimSpace(content[bestStart:])
	}

	const entryLookback = 220
	for _, pattern := range experienceEntryStartPatterns {
		loc := pattern.FindStringIndex(content)
		if loc == nil {
			continue
		}
		start := loc[0] - entryLookback
		if start < 0 {
			start = 0
		}
		if bestStart == -1 || start < bestStart {
			bestStart = start
		}
	}
	if bestStart >= 0 {
		return strings.TrimSpace(content[bestStart:])
	}

	return ""
}

func normalizeSkillKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		".", " ",
		"/", " ",
		"-", " ",
		"_", " ",
		"(", " ",
		")", " ",
		",", " ",
	)
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func normalizeContentForSkillMatch(value string) string {
	return " " + normalizeSkillKey(value) + " "
}

func skillCovered(content, skill string) bool {
	normalizedContent := normalizeContentForSkillMatch(content)
	skillKey := normalizeSkillKey(skill)
	aliases := skillAliases(skillKey)
	for _, alias := range aliases {
		if strings.Contains(normalizedContent, " "+normalizeSkillKey(alias)+" ") {
			return true
		}
	}
	return false
}

func skillAliases(skill string) []string {
	switch normalizeSkillKey(skill) {
	case "kotlin":
		return []string{"kotlin"}
	case "spring boot", "springboot":
		return []string{"spring boot", "springboot"}
	case "aws":
		return []string{
			"aws", "amazon web services", "ec2", "s3", "lambda", "cloudwatch",
			"api gateway", "apigateway", "dynamodb", "eks", "ecs", "route 53",
			"rds", "sns", "sqs", "iam", "vpc", "cloudformation", "codepipeline",
			"cognito", "elasticache", "eventbridge",
		}
	case "dart":
		return []string{"dart", "flutter"}
	case "flutter":
		return []string{"flutter", "dart"}
	case "react":
		return []string{"react", "reactjs", "react js", "react.js"}
	case "typescript":
		return []string{"typescript", "type script"}
	case "ci cd", "cicd":
		return []string{"ci cd", "cicd", "jenkins", "gitlab ci", "github actions", "azure devops"}
	case "agile":
		return []string{"agile", "scrum", "kanban", "safe"}
	default:
		return []string{skill}
	}
}

func parseScoreDocumentAnswerWithRegex(answer string) (int, string, string, bool) {
	scoreMatch := scoreFieldPattern.FindStringSubmatch(answer)
	if len(scoreMatch) != 2 {
		return 0, "", "unknown", false
	}
	score, err := strconv.Atoi(scoreMatch[1])
	if err != nil {
		return 0, "", "unknown", false
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	justification := ""
	if match := justificationFieldPattern.FindStringSubmatch(answer); len(match) == 2 {
		if unquoted, err := strconv.Unquote(`"` + match[1] + `"`); err == nil {
			justification = strings.TrimSpace(unquoted)
		} else {
			justification = strings.TrimSpace(match[1])
		}
	}

	seniorityAssessment := "unknown"
	if match := seniorityFieldPattern.FindStringSubmatch(answer); len(match) == 2 {
		seniorityAssessment = strings.ToLower(strings.TrimSpace(match[1]))
	}

	return score, justification, seniorityAssessment, true
}

func assessSeniority(content, label string) string {
	minYears, maxYears, ok := parseSeniorityRange(label)
	if !ok {
		return "unknown"
	}
	years, ok := extractExperienceYears(content)
	if !ok {
		return "unknown"
	}
	if years < minYears {
		return "below"
	}
	if maxYears > 0 && years > maxYears {
		return "above"
	}
	return "match"
}

func parseSeniorityRange(label string) (int, int, bool) {
	match := seniorityRangePattern.FindStringSubmatch(label)
	if len(match) != 3 {
		return 0, 0, false
	}
	minYears, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	maxYears, err := strconv.Atoi(match[2])
	if err != nil {
		return 0, 0, false
	}
	return minYears, maxYears, true
}

func extractExperienceYears(content string) (int, bool) {
	maxYears := 0
	for _, pattern := range experienceYearsPatterns {
		matches := pattern.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			years, err := strconv.Atoi(match[1])
			if err != nil {
				continue
			}
			if years > maxYears {
				maxYears = years
			}
		}
	}
	if maxYears == 0 {
		return 0, false
	}
	return maxYears, true
}

func truncateScoreDocumentString(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func intValue(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
