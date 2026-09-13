package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

var (
	emailPattern          = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	phonePattern          = regexp.MustCompile(`(?:\+33|0033|0)[1-9](?:[\s.-]?\d{2}){4}`)
	addressPattern        = regexp.MustCompile(`(?i)\b\d{1,4}[,\s]+(?:rue|avenue|boulevard|place|impasse|chemin|allee|allée)[\s\w,'-]+\d{5}\b`)
	birthPattern          = regexp.MustCompile(`(?i)\b(?:n[eé]|n[ée]e|born|naissance)\b[\s:]+(?:le\s+)?\d{1,2}[\s/.-]\d{1,2}[\s/.-]\d{2,4}`)
	linkedinPattern       = regexp.MustCompile(`(?i)linkedin\.com/in/[A-Za-z0-9_-]+`)
	githubPattern         = regexp.MustCompile(`(?i)github\.com/[A-Za-z0-9_-]+`)
	personalSocialPattern = regexp.MustCompile(`(?i)(?:facebook|instagram|twitter|tiktok)\.com/[A-Za-z0-9._-]+`)
)

var publicEmailDomains = map[string]struct{}{
	"gmail.com":      {},
	"googlemail.com": {},
	"yahoo.com":      {},
	"yahoo.fr":       {},
	"hotmail.com":    {},
	"hotmail.fr":     {},
	"outlook.com":    {},
	"outlook.fr":     {},
	"live.com":       {},
	"live.fr":        {},
	"msn.com":        {},
	"icloud.com":     {},
	"me.com":         {},
	"orange.fr":      {},
	"wanadoo.fr":     {},
	"free.fr":        {},
	"laposte.net":    {},
	"sfr.fr":         {},
	"bbox.fr":        {},
	"proton.me":      {},
	"protonmail.com": {},
	"pm.me":          {},
	"gmx.com":        {},
	"gmx.fr":         {},
	"aol.com":        {},
	"mail.com":       {},
}

var nameStopwords = map[string]struct{}{
	"cv": {}, "profil": {}, "profile": {}, "dossier": {}, "competence": {}, "competences": {}, "skills": {},
	"consultant": {}, "consultante": {}, "developpeur": {}, "developpeuse": {}, "developer": {},
	"engineer": {}, "ingenieur": {}, "ingenieure": {}, "architecte": {}, "architect": {}, "manager": {},
	"lead": {}, "senior": {}, "junior": {}, "expert": {}, "experte": {}, "java": {}, "spring": {},
	"react": {}, "angular": {}, "typescript": {}, "javascript": {}, "python": {}, "node": {}, "nodejs": {},
	"kotlin": {}, "scala": {}, "golang": {}, "cloud": {}, "aws": {}, "azure": {}, "gcp": {}, "docker": {},
	"kubernetes": {}, "backend": {}, "frontend": {}, "fullstack": {}, "full": {}, "stack": {}, "data": {},
	"bonjour": {}, "hello": {}, "cordialement": {}, "bonne": {}, "journee": {}, "reception": {}, "merci": {},
	"contact": {}, "commercial": {}, "trigramme": {}, "nom": {}, "prenom": {}, "telephone": {}, "tel": {},
	"mail": {}, "email": {}, "source": {}, "sources": {}, "fichier": {}, "file": {}, "offre": {},
}

type ScanDocumentTool struct {
	docRepo         *repository.DocumentRepository
	chunkRepo       *repository.ChunkRepository
	tenantRepo      *repository.TenantRepository
	inspector       inspect.ContentInspector
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

type scanIgnoreEntities struct {
	Emails       map[string]struct{}
	EmailDomains map[string]struct{}
	Phones       map[string]struct{}
	Names        map[string]struct{}
}

type scanDocumentInput struct {
	DocumentID string
	Title      string
	DocType    string
	Metadata   map[string]interface{}
	Chunks     []*repositoryChunkView
}

type scanRecorder struct {
	documentID          string
	includeSpans        bool
	includeChunkMeta    bool
	maxMatchesPerType   int
	ignore              scanIgnoreEntities
	matches             []map[string]interface{}
	ignoredMatches      []map[string]interface{}
	countsByType        map[string]int
	ignoredCountsByType map[string]int
	seen                map[string]struct{}
}

func NewScanDocumentTool(docRepo *repository.DocumentRepository, chunkRepo *repository.ChunkRepository, tenantRepo *repository.TenantRepository, inspector inspect.ContentInspector, responseWrapper *ResponseWrapper, logger *zap.Logger) *ScanDocumentTool {
	return &ScanDocumentTool{
		docRepo:         docRepo,
		chunkRepo:       chunkRepo,
		tenantRepo:      tenantRepo,
		inspector:       inspector,
		responseWrapper: responseWrapper,
		logger:          logger.Named("scan-document-tool"),
	}
}

func (t *ScanDocumentTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_scan_document",
		Description: "Scan a document or metadata-filtered document set exhaustively for PII with structured matches and optional ignore rules",
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
					"description": "Optional document ID to inspect directly",
				},
				"metadata_filters": map[string]interface{}{
					"type":        "object",
					"description": "Optional metadata filters to select matching documents/chunks. Example: {\"offre_id\": \"18889\"}",
					"additionalProperties": map[string]interface{}{
						"type": "string",
					},
				},
				"profile": map[string]interface{}{
					"type":    "string",
					"enum":    []string{"pii_basic", "pii_strict", "cv_identifiability"},
					"default": "pii_strict",
				},
				"include_spans": map[string]interface{}{
					"type":        "boolean",
					"description": "Include start/end offsets for regex-based matches (default: true)",
				},
				"include_chunk_metadata": map[string]interface{}{
					"type":        "boolean",
					"description": "Include chunk metadata in each match payload (default: false)",
				},
				"max_documents": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of documents to inspect when using metadata_filters (default: 10)",
				},
				"max_chunks": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of chunks to inspect across all documents (default: 500)",
				},
				"max_matches_per_type": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of unique matches to return for each type per document (default: 20)",
				},
				"ignore_entities": map[string]interface{}{
					"type":        "object",
					"description": "Entities to ignore from findings, e.g. commercial contacts from API data",
					"properties": map[string]interface{}{
						"emails": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
						"phones": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
						"names": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

func (t *ScanDocumentTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			DeploymentID      string            `json:"deployment_id"`
			TenantID          string            `json:"tenant_id"`
			DocumentID        string            `json:"document_id"`
			MetadataFilters   map[string]string `json:"metadata_filters"`
			Profile           string            `json:"profile"`
			IncludeSpans      *bool             `json:"include_spans"`
			IncludeChunkMeta  bool              `json:"include_chunk_metadata"`
			MaxDocuments      int               `json:"max_documents"`
			MaxChunks         int               `json:"max_chunks"`
			MaxMatchesPerType int               `json:"max_matches_per_type"`
			IgnoreEntities    struct {
				Emails []string `json:"emails"`
				Phones []string `json:"phones"`
				Names  []string `json:"names"`
			} `json:"ignore_entities"`
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
		if args.DocumentID == "" && len(args.MetadataFilters) == 0 {
			return errorResult("document_id or metadata_filters is required"), nil
		}
		if args.Profile == "" {
			args.Profile = "pii_strict"
		}
		if args.MaxDocuments <= 0 {
			args.MaxDocuments = 10
		}
		if args.MaxChunks <= 0 {
			args.MaxChunks = 500
		}
		if args.MaxMatchesPerType <= 0 {
			args.MaxMatchesPerType = 20
		}
		includeSpans := true
		if args.IncludeSpans != nil {
			includeSpans = *args.IncludeSpans
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		ignore := newScanIgnoreEntities(args.IgnoreEntities.Emails, args.IgnoreEntities.Phones, args.IgnoreEntities.Names)

		var docs []scanDocumentInput
		totalChunks := 0

		if args.DocumentID != "" {
			docs, totalChunks, err = t.collectByDocumentID(ctx, deploymentID, args.TenantID, args.DocumentID, args.MetadataFilters, args.MaxChunks)
		} else {
			docs, totalChunks, err = t.collectByMetadata(ctx, deploymentID, args.TenantID, args.MetadataFilters, args.MaxDocuments, args.MaxChunks)
		}
		if err != nil {
			t.logger.Error("document scan failed", zap.Error(err))
			return errorResult("document scan failed: " + err.Error()), nil
		}

		documents := make([]map[string]interface{}, 0, len(docs))
		totalMatches := 0
		totalIgnoredMatches := 0
		aggregateCounts := map[string]int{}

		for _, doc := range docs {
			scanned := scanDocumentPII(doc, args.Profile, includeSpans, args.IncludeChunkMeta, args.MaxMatchesPerType, ignore)
			documents = append(documents, scanned)
			if matches, ok := scanned["matches"].([]map[string]interface{}); ok {
				totalMatches += len(matches)
			}
			if ignored, ok := scanned["ignored_matches"].([]map[string]interface{}); ok {
				totalIgnoredMatches += len(ignored)
			}
			if counts, ok := scanned["scan_summary"].(map[string]interface{}); ok {
				if byType, ok := counts["counts_by_type"].(map[string]int); ok {
					for key, value := range byType {
						aggregateCounts[key] += value
					}
				}
			}
		}

		response := map[string]interface{}{
			"profile":             args.Profile,
			"document_count":      len(documents),
			"chunk_count":         totalChunks,
			"match_count":         totalMatches,
			"ignored_match_count": totalIgnoredMatches,
			"counts_by_type":      aggregateCounts,
			"documents":           documents,
			"metadata_filters":    args.MetadataFilters,
			"ignore_entities_applied": map[string]interface{}{
				"emails": args.IgnoreEntities.Emails,
				"phones": args.IgnoreEntities.Phones,
				"names":  args.IgnoreEntities.Names,
			},
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("Scanned %d documents, %d chunks, %d matches", len(documents), totalChunks, totalMatches)
			return t.responseWrapper.WrapToolResult(ctx, "rag_scan_document", response, summary)
		}

		respBytes, _ := json.Marshal(response)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: string(respBytes)},
			},
		}, nil
	}
}

func (t *ScanDocumentTool) collectByDocumentID(ctx context.Context, deploymentID xid.ID, tenantIDRaw, documentID string, metadataFilters map[string]string, maxChunks int) ([]scanDocumentInput, int, error) {
	docID, err := xid.FromString(documentID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid document_id")
	}
	doc, err := t.docRepo.Get(ctx, docID)
	if err != nil {
		return nil, 0, err
	}
	if tenantIDRaw != "" {
		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, tenantIDRaw, t.logger)
		if err != nil {
			return nil, 0, err
		}
		if doc.TenantID != tenantID {
			return nil, 0, fmt.Errorf("document does not belong to tenant")
		}
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
		return []scanDocumentInput{}, 0, nil
	}

	return []scanDocumentInput{{
		DocumentID: doc.ID.String(),
		Title:      doc.Title,
		DocType:    doc.DocType,
		Metadata:   doc.Metadata,
		Chunks:     filtered,
	}}, len(filtered), nil
}

func (t *ScanDocumentTool) collectByMetadata(ctx context.Context, deploymentID xid.ID, tenantIDRaw string, metadataFilters map[string]string, maxDocuments, maxChunks int) ([]scanDocumentInput, int, error) {
	tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, tenantIDRaw, t.logger)
	if err != nil {
		return nil, 0, err
	}

	const pageSize = 500
	docAccumulators := make(map[string]*scanDocumentInput)
	docOrder := make([]string, 0)
	totalChunks := 0

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
			acc := docAccumulators[key]
			if acc == nil {
				if maxDocuments > 0 && len(docOrder) >= maxDocuments {
					continue
				}
				doc, err := t.docRepo.Get(ctx, ch.DocumentID)
				if err != nil {
					return nil, 0, err
				}
				if doc.TenantID != tenantID {
					continue
				}
				acc = &scanDocumentInput{
					DocumentID: key,
					Title:      doc.Title,
					DocType:    doc.DocType,
					Metadata:   doc.Metadata,
					Chunks:     make([]*repositoryChunkView, 0),
				}
				docAccumulators[key] = acc
				docOrder = append(docOrder, key)
			}

			if maxChunks > 0 && totalChunks >= maxChunks {
				break
			}

			acc.Chunks = append(acc.Chunks, &repositoryChunkView{
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

	documents := make([]scanDocumentInput, 0, len(docOrder))
	for _, key := range docOrder {
		if acc := docAccumulators[key]; acc != nil && len(acc.Chunks) > 0 {
			documents = append(documents, *acc)
		}
	}

	return documents, totalChunks, nil
}

func scanDocumentPII(doc scanDocumentInput, profile string, includeSpans, includeChunkMetadata bool, maxMatchesPerType int, ignore scanIgnoreEntities) map[string]interface{} {
	rec := &scanRecorder{
		documentID:          doc.DocumentID,
		includeSpans:        includeSpans,
		includeChunkMeta:    includeChunkMetadata,
		maxMatchesPerType:   maxMatchesPerType,
		ignore:              ignore,
		matches:             make([]map[string]interface{}, 0),
		ignoredMatches:      make([]map[string]interface{}, 0),
		countsByType:        make(map[string]int),
		ignoredCountsByType: make(map[string]int),
		seen:                make(map[string]struct{}),
	}

	for _, chunk := range doc.Chunks {
		rec.scanChunk(chunk)
	}
	rec.scanNames(doc)

	summary := map[string]interface{}{
		"pii_detected":        len(rec.matches) > 0,
		"risk_level":          computeRiskLevel(rec.countsByType),
		"counts_by_type":      rec.countsByType,
		"ignored_by_type":     rec.ignoredCountsByType,
		"match_count":         len(rec.matches),
		"ignored_match_count": len(rec.ignoredMatches),
		"profile":             profile,
	}

	return map[string]interface{}{
		"document_id":     doc.DocumentID,
		"title":           doc.Title,
		"doc_type":        doc.DocType,
		"metadata":        doc.Metadata,
		"chunk_count":     len(doc.Chunks),
		"scan_summary":    summary,
		"matches":         rec.matches,
		"ignored_matches": rec.ignoredMatches,
	}
}

func (r *scanRecorder) scanChunk(chunk *repositoryChunkView) {
	if chunk == nil {
		return
	}
	text := chunk.Text
	r.scanRegexpMatches(chunk, text, emailPattern, func(value string) (string, string, bool) {
		switch {
		case r.ignoreEmail(value):
			return "ignored_email", "ignore_entities", true
		case isLikelyProfessionalEmail(value, r.ignore):
			return "professional_email", "regex", false
		default:
			return "personal_email", "regex", false
		}
	})
	r.scanRegexpMatches(chunk, text, phonePattern, func(value string) (string, string, bool) {
		if r.ignorePhone(value) {
			return "ignored_phone", "ignore_entities", true
		}
		return "personal_phone_number", "regex", false
	})
	r.scanRegexpMatches(chunk, text, addressPattern, func(value string) (string, string, bool) {
		return "personal_address", "regex", false
	})
	r.scanRegexpMatches(chunk, text, birthPattern, func(value string) (string, string, bool) {
		return "birth_date", "regex", false
	})
	r.scanRegexpMatches(chunk, text, linkedinPattern, func(value string) (string, string, bool) {
		return "professional_social_media", "regex", false
	})
	r.scanRegexpMatches(chunk, text, githubPattern, func(value string) (string, string, bool) {
		return "professional_social_media", "regex", false
	})
	r.scanRegexpMatches(chunk, text, personalSocialPattern, func(value string) (string, string, bool) {
		return "personal_social_media", "regex", false
	})
}

func (r *scanRecorder) scanRegexpMatches(chunk *repositoryChunkView, text string, re *regexp.Regexp, classify func(value string) (matchType, source string, ignored bool)) {
	locs := re.FindAllStringIndex(text, -1)
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		value := strings.TrimSpace(text[start:end])
		if value == "" {
			continue
		}
		matchType, source, ignored := classify(value)
		quote := snippetWithBounds(text, start, end)
		if ignored {
			r.recordIgnored(matchType, value, source, chunk, quote)
			continue
		}
		r.recordMatch(matchType, value, source, riskForType(matchType), chunk, quote, start, end)
	}
}

func (r *scanRecorder) scanNames(doc scanDocumentInput) {
	filename := normalizeFilenameForScan(stringValue(doc.Metadata["file_name"]))
	if filename != "" {
		for _, token := range extractFilenameNameTokens(filename) {
			if r.ignoreName(token) {
				r.recordIgnored("name_in_file", token, "filename", nil, filename)
				continue
			}
			r.recordMatch("name_in_file", token, "filename", riskForType("name_in_file"), nil, filename, -1, -1)
		}
	}

	joined := joinChunkTexts(doc.Chunks)
	lines := strings.Split(joined, "\n")
	for idx, rawLine := range lines {
		if idx >= 25 {
			break
		}
		line := strings.TrimSpace(rawLine)
		if line == "" || len(line) > 120 || strings.Contains(line, "@") || strings.Contains(strings.ToLower(line), "linkedin") || strings.Contains(strings.ToLower(line), "github") {
			continue
		}
		if regexp.MustCompile(`\d{3,}`).MatchString(line) {
			continue
		}
		tokens := extractNameTokens(line)
		if len(tokens) == 0 {
			continue
		}
		token := tokens[0]
		if r.ignoreName(token) {
			r.recordIgnored("contact_name", token, "header", firstChunk(doc.Chunks), line)
			continue
		}
		r.recordMatch("contact_name", token, "header", riskForType("contact_name"), firstChunk(doc.Chunks), line, -1, -1)
		break
	}
}

func (r *scanRecorder) recordMatch(matchType, value, source, risk string, chunk *repositoryChunkView, quote string, start, end int) {
	normalizedValue := normalizeMatchKey(matchType, value)
	if normalizedValue == "" {
		return
	}
	key := matchType + "::" + normalizedValue
	if _, exists := r.seen[key]; exists {
		return
	}
	if r.maxMatchesPerType > 0 && r.countsByType[matchType] >= r.maxMatchesPerType {
		return
	}
	r.seen[key] = struct{}{}
	payload := map[string]interface{}{
		"type":        matchType,
		"value":       value,
		"source":      source,
		"risk":        risk,
		"document_id": r.documentID,
		"text_quote":  quote,
	}
	if chunk != nil {
		payload["chunk_id"] = chunk.ID
		if r.includeChunkMeta {
			payload["chunk_metadata"] = chunk.Metadata
		}
	}
	if r.includeSpans && start >= 0 && end >= 0 {
		payload["start_offset"] = start
		payload["end_offset"] = end
	}
	r.matches = append(r.matches, payload)
	r.countsByType[matchType]++
}

func (r *scanRecorder) recordIgnored(matchType, value, source string, chunk *repositoryChunkView, quote string) {
	payload := map[string]interface{}{
		"type":        matchType,
		"value":       value,
		"source":      source,
		"document_id": r.documentID,
		"text_quote":  quote,
	}
	if chunk != nil {
		payload["chunk_id"] = chunk.ID
		if r.includeChunkMeta {
			payload["chunk_metadata"] = chunk.Metadata
		}
	}
	r.ignoredMatches = append(r.ignoredMatches, payload)
	r.ignoredCountsByType[matchType]++
}

func (r *scanRecorder) ignoreEmail(value string) bool {
	normalized := normalizeEmailValue(value)
	if normalized == "" {
		return false
	}
	if _, ok := r.ignore.Emails[normalized]; ok {
		return true
	}
	return false
}

func (r *scanRecorder) ignorePhone(value string) bool {
	core := normalizePhoneCoreValue(value)
	if core == "" {
		return false
	}
	_, ok := r.ignore.Phones[core]
	return ok
}

func (r *scanRecorder) ignoreName(value string) bool {
	normalized := normalizeNameValue(value)
	if normalized == "" {
		return false
	}
	_, ok := r.ignore.Names[normalized]
	return ok
}

func newScanIgnoreEntities(emails, phones, names []string) scanIgnoreEntities {
	result := scanIgnoreEntities{
		Emails:       make(map[string]struct{}),
		EmailDomains: make(map[string]struct{}),
		Phones:       make(map[string]struct{}),
		Names:        make(map[string]struct{}),
	}
	for _, email := range emails {
		normalized := normalizeEmailValue(email)
		if normalized == "" {
			continue
		}
		result.Emails[normalized] = struct{}{}
		if domain := normalizeEmailDomainValue(normalized); domain != "" {
			result.EmailDomains[domain] = struct{}{}
		}
	}
	for _, phone := range phones {
		if core := normalizePhoneCoreValue(phone); core != "" {
			result.Phones[core] = struct{}{}
		}
	}
	for _, name := range names {
		if normalized := normalizeNameValue(name); normalized != "" {
			result.Names[normalized] = struct{}{}
			for _, token := range strings.Fields(normalized) {
				result.Names[token] = struct{}{}
			}
		}
	}
	return result
}

func isLikelyProfessionalEmail(value string, ignore scanIgnoreEntities) bool {
	domain := normalizeEmailDomainValue(value)
	if domain == "" {
		return false
	}
	if _, ok := publicEmailDomains[domain]; ok {
		return false
	}
	if _, ok := ignore.EmailDomains[domain]; ok {
		return true
	}
	return true
}

func normalizeEmailValue(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func normalizeEmailDomainValue(value string) string {
	normalized := normalizeEmailValue(value)
	at := strings.LastIndex(normalized, "@")
	if at < 0 || at == len(normalized)-1 {
		return ""
	}
	return normalized[at+1:]
}

func normalizePhoneCoreValue(value string) string {
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	normalized := digits.String()
	if normalized == "" {
		return ""
	}
	if strings.HasPrefix(normalized, "0033") {
		normalized = normalized[4:]
	} else if strings.HasPrefix(normalized, "33") {
		normalized = normalized[2:]
	}
	if strings.HasPrefix(normalized, "0") && len(normalized) >= 10 {
		normalized = normalized[1:]
	}
	if len(normalized) > 9 {
		normalized = normalized[len(normalized)-9:]
	}
	return normalized
}

func normalizeNameValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("-", " ", "_", " ", ".", " ", ",", " ", "'", " ", "’", " ", "/", " ")
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func normalizeMatchKey(matchType, value string) string {
	switch matchType {
	case "personal_email", "professional_email":
		return normalizeEmailValue(value)
	case "personal_phone_number":
		return normalizePhoneCoreValue(value)
	default:
		return normalizeNameValue(value)
	}
}

func normalizeFilenameForScan(filename string) string {
	filename = strings.TrimSpace(filename)
	filename = regexp.MustCompile(`\.[a-z0-9]+$`).ReplaceAllString(filename, "")
	filename = regexp.MustCompile(`___[a-f0-9]+$`).ReplaceAllString(filename, "")
	return filename
}

func extractNameTokens(text string) []string {
	rawTokens := strings.FieldsFunc(text, func(r rune) bool {
		switch {
		case r >= 'A' && r <= 'Z':
			return false
		case r >= 'a' && r <= 'z':
			return false
		case r >= 'À' && r <= 'ÿ':
			return false
		case r == '\'' || r == '’' || r == '-':
			return false
		default:
			return true
		}
	})

	tokens := make([]string, 0, len(rawTokens))
	for _, token := range rawTokens {
		normalized := normalizeNameValue(token)
		if normalized == "" {
			continue
		}
		if len(normalized) < 2 || len(normalized) > 30 {
			continue
		}
		if _, blocked := nameStopwords[normalized]; blocked {
			continue
		}
		if regexp.MustCompile(`^[A-Z]{2,6}$`).MatchString(token) {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func extractFilenameNameTokens(text string) []string {
	parts := regexp.MustCompile(`[-_\s]+`).Split(text, -1)
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		tokens = append(tokens, extractNameTokens(part)...)
	}
	return tokens
}

func joinChunkTexts(chunks []*repositoryChunkView) string {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == nil || strings.TrimSpace(chunk.Text) == "" {
			continue
		}
		parts = append(parts, chunk.Text)
	}
	return strings.Join(parts, "\n")
}

func firstChunk(chunks []*repositoryChunkView) *repositoryChunkView {
	if len(chunks) == 0 {
		return nil
	}
	return chunks[0]
}

func snippetWithBounds(text string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	left := start - 40
	if left < 0 {
		left = 0
	}
	right := end + 40
	if right > len(text) {
		right = len(text)
	}
	return strings.TrimSpace(text[left:right])
}

func riskForType(matchType string) string {
	switch matchType {
	case "birth_date", "personal_social_media", "personal_address":
		return "high"
	case "contact_name", "name_in_file", "personal_email", "personal_phone_number":
		return "medium"
	default:
		return "low"
	}
}

func computeRiskLevel(counts map[string]int) string {
	if counts["birth_date"] > 0 || counts["personal_social_media"] > 0 || counts["personal_address"] > 0 {
		return "high"
	}
	if counts["contact_name"] > 0 || counts["name_in_file"] > 0 || counts["personal_email"] > 0 || counts["personal_phone_number"] > 0 {
		return "medium"
	}
	if len(counts) > 0 {
		return "low"
	}
	return "low"
}
