package inspect

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

type mockLLM struct{ mock.Mock }

func (m *mockLLM) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	args := m.Called(ctx, text)
	return args.Get(0).([]float64), args.Error(1)
}
func (m *mockLLM) GenerateAnswer(ctx context.Context, prompt string, maxTokens int) (string, error) {
	args := m.Called(ctx, prompt, maxTokens)
	return args.String(0), args.Error(1)
}

func TestInspector_Regex(t *testing.T) {
	llm := new(mockLLM)
	inspector := NewInspector(llm, zap.NewNop())
	rules := DefaultProfile("pii_basic")
	res, err := inspector.Inspect(context.Background(), "dep", "tenant", "contact: test@example.com", nil, rules)
	assert.NoError(t, err)
	assert.True(t, res.HasFindings)
}

func TestDefaultProfileCVIdentifiabilityIsNonBlocking(t *testing.T) {
	rules := DefaultProfile("cv_identifiability")
	if assert.Len(t, rules, 1) {
		assert.Equal(t, "warn", rules[0].Severity)
	}
}
