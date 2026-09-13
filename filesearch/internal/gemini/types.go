package gemini

import "time"

// FileSearchStore represents a Gemini FileSearch store.
type FileSearchStore struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	CreateTime  time.Time `json:"createTime"`
	UpdateTime  time.Time `json:"updateTime"`
}

// File represents a file in Gemini.
type File struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName,omitempty"`
	MimeType    string    `json:"mimeType"`
	SizeBytes   int64     `json:"sizeBytes,string,omitempty"`
	State       string    `json:"state"` // PROCESSING, ACTIVE, FAILED
	CreateTime  time.Time `json:"createTime"`
	UpdateTime  time.Time `json:"updateTime"`
	URI         string    `json:"uri,omitempty"`
	Error       *FileError `json:"error,omitempty"`
}

// FileError represents an error state for a file.
type FileError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// CreateStoreRequest is the request body for creating a store.
type CreateStoreRequest struct {
	DisplayName string `json:"displayName"`
}

// CreateStoreResponse is the response from creating a store.
type CreateStoreResponse struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	CreateTime  time.Time `json:"createTime"`
	UpdateTime  time.Time `json:"updateTime"`
}

// UploadFileRequest is the request for uploading a file via URL.
type UploadFileRequest struct {
	File struct {
		DisplayName string `json:"displayName,omitempty"`
	} `json:"file"`
}

// Content represents content in a generate request.
type Content struct {
	Parts []Part `json:"parts"`
	Role  string `json:"role,omitempty"`
}

// Part represents a part of content.
type Part struct {
	Text string `json:"text"`
}

// Tool represents a tool configuration.
type Tool struct {
	FileSearch *FileSearchConfig `json:"fileSearch,omitempty"`
}

// FileSearchConfig configures the file search tool.
type FileSearchConfig struct {
	DynamicRetrievalConfig *DynamicRetrievalConfig `json:"dynamicRetrievalConfig,omitempty"`
}

// DynamicRetrievalConfig configures dynamic retrieval.
type DynamicRetrievalConfig struct {
	Mode               string `json:"mode,omitempty"`
	DynamicThreshold   float64 `json:"dynamicThreshold,omitempty"`
}

// GenerateContentRequest is the request for generating content.
type GenerateContentRequest struct {
	Contents       []Content       `json:"contents"`
	Tools          []Tool          `json:"tools,omitempty"`
	SystemInstruction *Content     `json:"systemInstruction,omitempty"`
}

// GenerateContentResponse is the response from content generation.
type GenerateContentResponse struct {
	Candidates []Candidate `json:"candidates"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
}

// Candidate represents a response candidate.
type Candidate struct {
	Content       Content       `json:"content"`
	FinishReason  string        `json:"finishReason"`
	SafetyRatings []SafetyRating `json:"safetyRatings,omitempty"`
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
}

// SafetyRating represents a safety rating.
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// GroundingMetadata contains grounding information.
type GroundingMetadata struct {
	GroundingChunks []GroundingChunk `json:"groundingChunks,omitempty"`
	GroundingSupports []GroundingSupport `json:"groundingSupports,omitempty"`
	WebSearchQueries []string `json:"webSearchQueries,omitempty"`
}

// GroundingChunk represents a chunk from grounding.
type GroundingChunk struct {
	Web *WebChunk `json:"web,omitempty"`
}

// WebChunk represents a web-based chunk.
type WebChunk struct {
	URI   string `json:"uri"`
	Title string `json:"title"`
}

// GroundingSupport represents support information.
type GroundingSupport struct {
	GroundingChunkIndices []int `json:"groundingChunkIndices,omitempty"`
	ConfidenceScores      []float64 `json:"confidenceScores,omitempty"`
	Segment               *Segment `json:"segment,omitempty"`
}

// Segment represents a segment of content.
type Segment struct {
	PartIndex  int `json:"partIndex"`
	StartIndex int `json:"startIndex"`
	EndIndex   int `json:"endIndex"`
	Text       string `json:"text"`
}

// UsageMetadata contains usage information.
type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// ErrorResponse represents an error from the API.
type ErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
