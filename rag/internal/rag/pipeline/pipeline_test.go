package pipeline

import (
	"context"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/experience"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/queryint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/reason"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/retrieve"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/rs/xid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

type MockVectorStore struct {
	mock.Mock
}

func (m *MockVectorStore) Search(ctx context.Context, query string, limit int, tenantID xid.ID) ([]shared.Chunk, error) {
	args := m.Called(ctx, query, limit, tenantID)
	return args.Get(0).([]shared.Chunk), args.Error(1)
}

func (m *MockVectorStore) Upsert(ctx context.Context, chunks []shared.Chunk) error {
	args := m.Called(ctx, chunks)
	return args.Error(0)
}

func (m *MockVectorStore) UpsertDocEmbedding(ctx context.Context, doc shared.Chunk) error {
	args := m.Called(ctx, doc)
	return args.Error(0)
}

func (m *MockVectorStore) Delete(ctx context.Context, chunkIDs []string) error {
	args := m.Called(ctx, chunkIDs)
	return args.Error(0)
}

func (m *MockVectorStore) DeleteByTenant(ctx context.Context, tenantID xid.ID) error {
	args := m.Called(ctx, tenantID)
	return args.Error(0)
}

func (m *MockVectorStore) SearchByVector(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, metadataFilters map[string]string) ([]shared.Chunk, error) {
	args := m.Called(ctx, embedding, topK, tenantID, metadataFilters)
	return args.Get(0).([]shared.Chunk), args.Error(1)
}

func (m *MockVectorStore) SearchDocEmbedding(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]shared.Chunk, error) {
	args := m.Called(ctx, embedding, topK, tenantID)
	return args.Get(0).([]shared.Chunk), args.Error(1)
}

type MockLLMClient struct {
	mock.Mock
}

func (m *MockLLMClient) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	args := m.Called(ctx, text)
	return args.Get(0).([]float64), args.Error(1)
}

func (m *MockLLMClient) GenerateAnswer(ctx context.Context, prompt string, maxTokens int) (string, error) {
	args := m.Called(ctx, prompt, maxTokens)
	return args.String(0), args.Error(1)
}

func TestPipeline_Query(t *testing.T) {
	// Setup
	logger := zap.NewNop()
	mockVS := new(MockVectorStore)
	mockLLM := new(MockLLMClient)

	// Initialize components with mocks
	docAnalyzer := docint.NewAnalyzer(logger)
	queryAnalyzer := queryint.NewAnalyzer(logger)
	noopCache := cache.NewNoopCache()
	floatRetriever := retrieve.NewFloatRetriever(mockVS, logger)
	retriever := retrieve.NewRetriever(floatRetriever, nil, mockLLM, noopCache, logger)
	answerer := reason.NewAnswerer(mockLLM, noopCache, logger)
	expLogger := experience.NewLogger(nil, logger)

	p := NewPipeline(docAnalyzer, queryAnalyzer, retriever, answerer, expLogger, logger)

	// Expectations
	ctx := context.Background()
	query := shared.Query{
		Text:         "test query",
		TenantID:     xid.New(),
		DeploymentID: xid.New(),
		MaxTokens:    100,
	}

	// Mock LLM for query analysis (if it uses LLM, but it might be simple keyword extraction)
	// Note: queryint.Analyzer might not use LLM, checking its implementation would be good,
	// but assuming for now it doesn't or we can't mock it easily without interfaces.
	// If it fails, we'll see.

	// Mock LLM for retrieval (embedding)
	// Wait, retrieve.Retriever.RetrieveWithReranking calls GenerateEmbedding?
	// Let's check retrieve.go. If it calls Search with string, maybe VectorStore handles embedding?
	// The interface says Search(ctx, query string, topK int).
	// So Retriever probably calls VectorStore.Search directly with string.
	// But wait, my previous mock expectation was GenerateEmbedding.
	// Let's look at the error I got: "mockVS does not implement shared.VectorStore".
	// The interface has Search(ctx, query string, topK int).
	// So I don't need to mock GenerateEmbedding for retrieval if VectorStore handles it,
	// OR if Retriever converts it.
	// Let's check retrieve.go to be sure.

	// Mock LLM for retrieval (embedding)
	embedding := []float64{0.1, 0.2}
	mockLLM.On("GenerateEmbedding", mock.Anything, mock.AnythingOfType("string")).Return(embedding, nil)

	// Mock VectorStore SearchByVector
	chunks := []shared.Chunk{{ID: "1", Text: "content"}}
	mockVS.On("SearchByVector", mock.Anything, embedding, 10, query.TenantID, mock.Anything).Return(chunks, nil)

	// Mock LLM for answer generation
	mockLLM.On("GenerateAnswer", mock.Anything, mock.Anything, 100).Return("test answer", nil)

	// Execute
	answer, err := p.Query(ctx, query)

	// Verify
	assert.NoError(t, err)
	assert.NotNil(t, answer)
	assert.Equal(t, "test answer", answer.Text)

	mockVS.AssertExpectations(t)
	mockLLM.AssertExpectations(t)
}
