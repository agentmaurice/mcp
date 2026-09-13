package business

import (
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/stretchr/testify/assert"
)

func TestShouldBlockOnFindingCVIdentifiabilityDoesNotBlockOnScore(t *testing.T) {
	worker := &IngestWorker{identBlockThreshold: 0.8}
	finding := inspect.DetectionFinding{
		Severity:                "warn",
		IdentificationRiskScore: 0.95,
	}

	assert.False(t, worker.shouldBlockOnFinding("cv_identifiability", finding))
}

func TestShouldBlockOnFindingBlocksStrictSeverity(t *testing.T) {
	worker := &IngestWorker{identBlockThreshold: 0.8}
	finding := inspect.DetectionFinding{
		Severity:                "block",
		IdentificationRiskScore: 0.10,
	}

	assert.True(t, worker.shouldBlockOnFinding("pii_strict", finding))
}

func TestShouldBlockOnFindingBlocksHighScoreOutsideCVIdentifiability(t *testing.T) {
	worker := &IngestWorker{identBlockThreshold: 0.8}
	finding := inspect.DetectionFinding{
		Severity:                "warn",
		IdentificationRiskScore: 0.95,
	}

	assert.True(t, worker.shouldBlockOnFinding("pii_basic", finding))
}
