//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mistral"
	"go.uber.org/zap"
)

const (
	envAPIKeyPrimary   = "MCP_MODERATOR_MISTRAL_API_KEY"
	envAPIKeyFallback  = "MISTRAL_API_KEY"
	envInputDir        = "MCP_MODERATOR_IT_INPUT_DIR"
	envOutputDir       = "MCP_MODERATOR_IT_OUTPUT_DIR"
	envModelOverride   = "MCP_MODERATOR_IT_MODEL"
	envBaseURLOverride = "MCP_MODERATOR_IT_BASE_URL"
	envTablePath       = "MCP_MODERATOR_IT_TABLE_FILE"

	defaultInputDir  = "integration/testdata/input"
	defaultOutputDir = "integration/testdata/output"
	defaultTablePath = "integration/testdata/table.json"
	defaultModel     = "mistral-moderation-latest"
)

type moderationExpectation struct {
	Text     string `json:"text"`
	Category string `json:"category"`
}

func prepareMistralClient(t *testing.T) (*mistral.Client, config.MistralConfig) {
	t.Helper()

	apiKey := firstNonEmpty(os.Getenv(envAPIKeyPrimary), os.Getenv(envAPIKeyFallback))
	if strings.TrimSpace(apiKey) == "" {
		t.Skipf("Skipping integration test: set %s or %s", envAPIKeyPrimary, envAPIKeyFallback)
	}

	baseURL := firstNonEmpty(os.Getenv(envBaseURLOverride), "https://api.mistral.ai")
	model := firstNonEmpty(os.Getenv(envModelOverride), defaultModel)

	cfg := config.MistralConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Timeout: 15 * time.Second,
	}

	client, err := mistral.NewClient(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create Mistral client: %v", err)
	}

	return client, cfg
}

func TestIntegration_MistralModeration(t *testing.T) {
	client, cfg := prepareMistralClient(t)
	inputDir := resolvePath(firstNonEmpty(os.Getenv(envInputDir), defaultInputDir))
	outputDir := resolvePath(firstNonEmpty(os.Getenv(envOutputDir), defaultOutputDir))

	files, err := discoverTextFiles(inputDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("Skipping integration test: input directory %s does not exist", inputDir)
		}
		t.Fatalf("failed to discover input files: %v", err)
	}
	if len(files) == 0 {
		t.Skipf("Skipping integration test: no text files found in %s", inputDir)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to ensure output directory %s: %v", outputDir, err)
	}

	for _, filePath := range files {
		t.Run(filepath.Base(filePath), func(t *testing.T) {
			content, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("failed to read %s: %v", filePath, err)
			}

			request := mistral.ChatModerationRequest{
				Model: cfg.Model,
				Input: []mistral.ChatMessage{
					{
						Role: "user",
						Content: []mistral.ChatContentItem{
							{Type: "text", Text: string(content)},
						},
					},
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
			defer cancel()

			response, err := client.ModerateChat(ctx, request)
			if err != nil {
				t.Fatalf("moderation call failed: %v", err)
			}

			outputFile := filepath.Join(outputDir, replaceExt(filepath.Base(filePath), ".json"))
			if err := writeJSON(outputFile, response); err != nil {
				t.Fatalf("failed to write output %s: %v", outputFile, err)
			}
			t.Logf("moderation result written to %s", outputFile)
		})
	}
}

func TestIntegration_MistralModerationTable(t *testing.T) {
	client, cfg := prepareMistralClient(t)

	tablePath := resolvePath(firstNonEmpty(os.Getenv(envTablePath), defaultTablePath))
	payload, err := os.ReadFile(tablePath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("Skipping integration test: table file %s does not exist", tablePath)
	}
	if err != nil {
		t.Fatalf("failed to read table file %s: %v", tablePath, err)
	}

	var samples []moderationExpectation
	if err := json.Unmarshal(payload, &samples); err != nil {
		t.Fatalf("failed to decode table file %s: %v", tablePath, err)
	}
	if len(samples) == 0 {
		t.Skipf("Skipping integration test: table %s contains no entries", tablePath)
	}

	for idx, sample := range samples {
		sample := sample
		testName := fmt.Sprintf("sample_%02d", idx)
		t.Run(testName, func(t *testing.T) {
			text := strings.TrimSpace(sample.Text)
			if text == "" {
				t.Skip("empty sample text")
			}

			request := mistral.ChatModerationRequest{
				Model: cfg.Model,
				Input: []mistral.ChatMessage{
					{
						Role: "user",
						Content: []mistral.ChatContentItem{
							{Type: "text", Text: text},
						},
					},
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
			defer cancel()

			response, err := client.ModerateChat(ctx, request)
			if err != nil {
				t.Fatalf("moderation call failed: %v", err)
			}

			assertCategoryMatch(t, response, sample.Category)
		})
	}
}

func discoverTextFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".txt") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func replaceExt(name, newExt string) string {
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + newExt
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func resolvePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}

	if _, err := os.Stat(path); err == nil {
		return path
	}

	alt := filepath.Join("..", "..", path)
	if _, err := os.Stat(alt); err == nil {
		return alt
	}

	if parent := filepath.Dir(alt); parent != "" {
		if _, err := os.Stat(parent); err == nil {
			return alt
		}
	}

	return path
}

func assertCategoryMatch(t *testing.T, response *mistral.ChatModerationResponse, expected string) {
	t.Helper()

	expected = strings.ToLower(strings.TrimSpace(expected))
	flaggedCategories := gatherFlaggedCategories(response)

	overallFlagged := false
	for _, result := range response.Results {
		if result.Flagged {
			overallFlagged = true
			break
		}
		for _, isFlagged := range result.Categories {
			if isFlagged {
				overallFlagged = true
				break
			}
		}
		if overallFlagged {
			break
		}
	}

	if expected == "safe" {
		if overallFlagged {
			t.Fatalf("expected safe content but moderation flagged categories: %v", flaggedCategories)
		}
		return
	}

	if !overallFlagged {
		t.Fatalf("expected flagged content for %q but moderation returned none", expected)
	}

	hints := categoryKeywords(expected)
	if len(hints) == 0 {
		hints = []string{expected}
	}

	for _, category := range flaggedCategories {
		lower := strings.ToLower(category)
		normalized := strings.ReplaceAll(lower, "_", "")
		for _, hint := range hints {
			hintLower := strings.ToLower(strings.TrimSpace(hint))
			if hintLower == "" {
				continue
			}
			hintNormalized := strings.ReplaceAll(hintLower, "_", "")
			if strings.Contains(lower, hintLower) || strings.Contains(normalized, hintNormalized) {
				return
			}
		}
	}

	t.Fatalf("expected category %q but got flagged categories %v", expected, flaggedCategories)
}

func gatherFlaggedCategories(response *mistral.ChatModerationResponse) []string {
	categories := make(map[string]struct{})
	for _, result := range response.Results {
		for name, flagged := range result.Categories {
			if flagged {
				categories[name] = struct{}{}
			}
		}
	}
	if len(categories) == 0 {
		return nil
	}

	out := make([]string, 0, len(categories))
	for name := range categories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func categoryKeywords(expected string) []string {
	switch strings.ToLower(expected) {
	case "sexual":
		return []string{"sexual"}
	case "hate_and_discrimination":
		return []string{"hate", "harass", "abuse", "discrimination"}
	case "violence_and_threats":
		return []string{"violence", "threat"}
	case "dangerous_and_criminal_content":
		return []string{"criminal", "crime", "illicit", "danger"}
	case "selfharm":
		return []string{"self-harm", "self_harm", "selfharm", "suicide"}
	case "health":
		return []string{"health", "medical", "medicine"}
	case "financial":
		return []string{"financial", "fraud", "scam"}
	case "law":
		return []string{"law", "illegal"}
	case "pii":
		return []string{"pii", "personal_information", "personal_data", "sensitive"}
	default:
		return nil
	}
}
