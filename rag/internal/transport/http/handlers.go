package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// IngestRequest represents an HTTP ingestion request
type IngestRequest struct {
	DeploymentID            string                  `json:"deployment_id"`
	TenantID                string                  `json:"tenant_id"`
	Title                   string                  `json:"title"`
	Content                 string                  `json:"content"`
	SourceType              string                  `json:"source_type"`
	SourceURL               string                  `json:"source_url,omitempty"`
	SourceMetadata          map[string]interface{}  `json:"source_metadata,omitempty"`
	ChunkingStrategy        string                  `json:"chunking_strategy,omitempty"`
	DetectDuplicates        bool                    `json:"detect_duplicates,omitempty"`
	DuplicateStrategy       string                  `json:"duplicate_strategy,omitempty"`
	DocType                 string                  `json:"doc_type,omitempty"`
	ContentType             string                  `json:"content_type,omitempty"`
	Size                    int64                   `json:"size,omitempty"`
	DetectContent           bool                    `json:"detect_content,omitempty"`
	ContentDetectionProfile string                  `json:"content_detection_profile,omitempty"`
	CustomDetectionRules    []inspect.DetectionRule `json:"custom_detection_rules,omitempty"`
}

// IngestResponse represents an HTTP ingestion response
type IngestResponse struct {
	JobID      string `json:"job_id,omitempty"`
	DocumentID string `json:"document_id,omitempty"`
	TenantID   string `json:"tenant_id"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// QueryRequest represents an HTTP query request
type QueryRequest struct {
	DeploymentID string `json:"deployment_id"`
	TenantID     string `json:"tenant_id,omitempty"`
	Query        string `json:"query"`
	Language     string `json:"language,omitempty"`
	MaxTokens    int    `json:"max_tokens,omitempty"`
}

// QueryResponse represents an HTTP query response
type QueryResponse struct {
	Answer    string         `json:"answer"`
	Citations []CitationResp `json:"citations"`
}

// CitationResp represents a citation in HTTP response
type CitationResp struct {
	ChunkID    string                 `json:"chunk_id"`
	DocumentID string                 `json:"document_id"`
	Snippet    string                 `json:"snippet"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// IngestStatusResponse represents the job status response
type IngestStatusResponse struct {
	JobID              string                 `json:"job_id"`
	Status             string                 `json:"status"`
	Progress           int                    `json:"progress"`
	Message            string                 `json:"message,omitempty"`
	HasContentFindings bool                   `json:"has_content_findings,omitempty"`
	ContentFindings    map[string]interface{} `json:"content_findings,omitempty"`
}

// TenantCreateRequest represents tenant creation payload
type TenantCreateRequest struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

// TenantResponse represents tenant payload
type TenantResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DeploymentID string `json:"deployment_id"`
	IsDefault    bool   `json:"is_default"`
}

// DLQReplayRequest represents a replay request for one DLQ message.
type DLQReplayRequest struct {
	Sequence          uint64 `json:"sequence"`
	DeleteAfterReplay *bool  `json:"delete_after_replay,omitempty"`
}

// DLQReplayResponse represents replay response payload.
type DLQReplayResponse struct {
	Status            string `json:"status"`
	JobID             string `json:"job_id,omitempty"`
	DLQSequence       uint64 `json:"dlq_sequence"`
	PublishedSequence uint64 `json:"published_sequence"`
}

// DLQMessageResponse represents one DLQ message in listing API.
type DLQMessageResponse struct {
	Sequence         uint64    `json:"sequence"`
	JobID            string    `json:"job_id,omitempty"`
	Reason           string    `json:"reason"`
	CapturedAt       time.Time `json:"captured_at"`
	OriginalSubject  string    `json:"original_subject"`
	OriginalSequence uint64    `json:"original_sequence"`
	Deliveries       uint64    `json:"deliveries"`
}

// DLQMessagesResponse represents paginated DLQ listing response.
type DLQMessagesResponse struct {
	Stream        string               `json:"stream"`
	Subject       string               `json:"subject"`
	FirstSequence uint64               `json:"first_sequence"`
	LastSequence  uint64               `json:"last_sequence"`
	NextBeforeSeq uint64               `json:"next_before_seq,omitempty"`
	Count         int                  `json:"count"`
	Messages      []DLQMessageResponse `json:"messages"`
}

// handleIngestAPI handles REST API ingestion requests
func (s *Server) handleIngestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.logger.Debug("received ingestion request")

	// Parse request
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.logger.Error("failed to parse ingestion request", zap.Error(err))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request format"})
		return
	}

	// Validate required fields
	if req.DeploymentID == "" || req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deployment_id and title are required"})
		return
	}
	if req.Content == "" && req.SourceURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "either content or source_url is required"})
		return
	}

	// Parse deployment ID
	deploymentID, err := xid.FromString(req.DeploymentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid deployment_id format"})
		return
	}

	// Parse tenant ID if provided; if invalid, fall back to default
	var tenantID xid.ID
	if req.TenantID != "" {
		if parsedID, err := xid.FromString(req.TenantID); err == nil {
			tenantID = parsedID
		} else {
			s.logger.Debug("tenant_id not valid xid, using deployment default", zap.String("tenant_id", req.TenantID))
		}
	}
	if tenantID == xid.NilID() {
		defaultTenant, err := s.tenantRepo.GetDefaultByDeployment(r.Context(), deploymentID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no default tenant for deployment"})
			return
		}
		tenantID = defaultTenant.ID
	}

	// Determine source type
	sourceType := req.SourceType
	if sourceType == "" {
		if req.Content != "" {
			sourceType = "text"
		} else {
			sourceType = "url"
		}
	}

	// Create ingest source
	source := business.IngestSource{
		Type:                    sourceType,
		URL:                     req.SourceURL,
		Content:                 req.Content,
		Title:                   req.Title,
		Metadata:                req.SourceMetadata,
		DetectDuplicates:        req.DetectDuplicates,
		DuplicateStrategy:       req.DuplicateStrategy,
		DocType:                 req.DocType,
		ContentType:             req.ContentType,
		Size:                    req.Size,
		DetectContent:           req.DetectContent,
		ContentDetectionProfile: req.ContentDetectionProfile,
		CustomDetectionRules:    req.CustomDetectionRules,
	}

	// Start ingestion
	result, err := s.ingestManager.StartIngest(r.Context(), deploymentID, tenantID, source)
	if err != nil {
		s.logger.Error("ingestion failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := IngestResponse{
		TenantID: tenantID.String(),
		Status:   result.Status,
		Message:  result.Message,
	}
	if result.JobID.String() != "00000000000000000000" {
		resp.JobID = result.JobID.String()
	}
	if result.DocumentID.String() != "00000000000000000000" {
		resp.DocumentID = result.DocumentID.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleIngestStatusAPI handles REST API job status requests
func (s *Server) handleIngestStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Extract job ID from path: /api/ingest/{id}
	path := r.URL.Path
	jobIDStr := strings.TrimPrefix(path, "/api/ingest/")
	if jobIDStr == "" || jobIDStr == path {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job_id is required"})
		return
	}

	s.logger.Debug("received job status request", zap.String("jobID", jobIDStr))

	// Parse job ID
	jobID, err := xid.FromString(jobIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job_id format"})
		return
	}

	// Get job status
	job, err := s.ingestManager.GetJobStatus(r.Context(), jobID)
	if err != nil {
		s.logger.Error("failed to get job status", zap.Error(err))
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	writeJSON(w, http.StatusOK, IngestStatusResponse{
		JobID:              job.ID.String(),
		Status:             string(job.Status),
		Progress:           job.Progress,
		Message:            job.Message,
		HasContentFindings: job.HasContentFindings,
		ContentFindings:    job.ContentFindings,
	})
}

// handleQueryAPI handles REST API query requests
func (s *Server) handleQueryAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.logger.Debug("received query request")

	// Parse request
	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.logger.Error("failed to parse query request", zap.Error(err))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request format"})
		return
	}

	// Validate required fields
	if req.DeploymentID == "" || req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deployment_id and query are required"})
		return
	}

	// Parse deployment ID
	deploymentID, err := xid.FromString(req.DeploymentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid deployment_id format"})
		return
	}

	// Parse or resolve tenant ID; if invalid provided, fall back to default
	var tenantID xid.ID
	if req.TenantID != "" {
		if parsedID, err := xid.FromString(req.TenantID); err == nil {
			tenantID = parsedID
		} else {
			s.logger.Debug("tenant_id not valid xid, using deployment default", zap.String("tenant_id", req.TenantID))
		}
	}
	if tenantID == xid.NilID() {
		defaultTenant, err := s.tenantRepo.GetDefaultByDeployment(r.Context(), deploymentID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no default tenant for deployment"})
			return
		}
		tenantID = defaultTenant.ID
	}

	// Create query
	query := shared.Query{
		Text:         req.Query,
		Language:     req.Language,
		MaxTokens:    req.MaxTokens,
		TenantID:     tenantID,
		DeploymentID: deploymentID,
	}

	// Set defaults
	if query.Language == "" {
		query.Language = "fr"
	}
	if query.MaxTokens == 0 {
		query.MaxTokens = 500
	}

	// Execute query
	answer, err := s.ragManager.Query(r.Context(), query)
	if err != nil {
		s.logger.Error("query failed", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Convert citations
	citations := make([]CitationResp, len(answer.Citations))
	for i, cite := range answer.Citations {
		citations[i] = CitationResp{
			ChunkID:    cite.ChunkID,
			DocumentID: cite.DocumentID.String(),
			Snippet:    cite.Snippet,
			Metadata:   cite.Metadata,
		}
	}

	writeJSON(w, http.StatusOK, QueryResponse{
		Answer:    answer.Text,
		Citations: citations,
	})
}

// handleDeploymentTenantsAPI handles listing/creating tenants for a deployment
func (s *Server) handleDeploymentTenantsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Extract deployment ID from path: /api/deployments/{id}/tenants
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "deployments" || parts[3] != "tenants" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	deploymentIDStr := parts[2]
	if deploymentIDStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deployment_id is required"})
		return
	}
	deploymentID, err := xid.FromString(deploymentIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid deployment_id format"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		tenants, err := s.tenantRepo.ListByDeployment(r.Context(), deploymentID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp := make([]TenantResponse, 0, len(tenants))
		for _, t := range tenants {
			resp = append(resp, TenantResponse{
				ID:           t.ID.String(),
				Name:         t.Name,
				DeploymentID: t.DeploymentID.String(),
				IsDefault:    t.IsDefault,
			})
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		var req TenantCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		name := req.Name
		if name == "" {
			name = "tenant-" + xid.New().String()
		}
		apiKey := xid.New().String()
		tenant, err := s.tenantRepo.CreateForDeployment(r.Context(), deploymentID, name, apiKey, req.IsDefault)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp := TenantResponse{
			ID:           tenant.ID.String(),
			Name:         tenant.Name,
			DeploymentID: tenant.DeploymentID.String(),
			IsDefault:    tenant.IsDefault,
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Server) handleDLQMessagesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.dlqInspector == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "dlq listing not available"})
		return
	}

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive integer"})
			return
		}
		limit = v
	}

	var beforeSeq uint64
	if raw := strings.TrimSpace(r.URL.Query().Get("before_seq")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before_seq must be a positive integer"})
			return
		}
		beforeSeq = v
	}

	result, err := s.dlqInspector.ListDLQMessages(r.Context(), limit, beforeSeq)
	if err != nil {
		s.logger.Error("failed to list dlq messages", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	items := make([]DLQMessageResponse, 0, len(result.Messages))
	for _, m := range result.Messages {
		item := DLQMessageResponse{
			Sequence:         m.Sequence,
			Reason:           m.Reason,
			CapturedAt:       m.CapturedAt,
			OriginalSubject:  m.OriginalSubject,
			OriginalSequence: m.OriginalSequence,
			Deliveries:       m.Deliveries,
		}
		if m.JobID != xid.NilID() {
			item.JobID = m.JobID.String()
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, DLQMessagesResponse{
		Stream:        result.Stream,
		Subject:       result.Subject,
		FirstSequence: result.FirstSequence,
		LastSequence:  result.LastSequence,
		NextBeforeSeq: result.NextBeforeSeq,
		Count:         len(items),
		Messages:      items,
	})
}

func (s *Server) handleDLQReplayAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.dlqReplayer == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "dlq replay not available"})
		return
	}

	var req DLQReplayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request format"})
		return
	}
	if req.Sequence == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sequence is required and must be > 0"})
		return
	}

	deleteAfterReplay := true
	if req.DeleteAfterReplay != nil {
		deleteAfterReplay = *req.DeleteAfterReplay
	}

	result, err := s.dlqReplayer.ReplayDLQMessage(r.Context(), req.Sequence, deleteAfterReplay)
	if err != nil {
		s.logger.Error("failed to replay dlq message",
			zap.Uint64("dlq_sequence", req.Sequence),
			zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := DLQReplayResponse{
		Status:            "replayed",
		DLQSequence:       result.DLQSequence,
		PublishedSequence: result.PublishedSequence,
	}
	if result.JobID != xid.NilID() {
		resp.JobID = result.JobID.String()
	}
	writeJSON(w, http.StatusOK, resp)
}
