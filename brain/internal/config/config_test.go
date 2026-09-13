package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestLoadOpenAICompatibleEmbeddingFromEnvironment(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("BRAIN_EMBEDDING_PROVIDER", "openai-compatible")
	t.Setenv("BRAIN_EMBEDDING_MODEL", "bge-m3")
	t.Setenv("BRAIN_EMBEDDING_DIMENSIONS", "1024")
	t.Setenv("BRAIN_EMBEDDING_BATCH_SIZE", "16")
	t.Setenv("BRAIN_OPENAI_BASE_URL", "https://provider.example/v1")
	t.Setenv("BRAIN_OPENAI_API_KEY", "runtime-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Embedding.Provider != "openai-compatible" || cfg.Embedding.Model != "bge-m3" {
		t.Fatalf("unexpected provider config: %#v", cfg.Embedding)
	}
	if cfg.Embedding.Dimensions != 1024 || cfg.Embedding.BatchSize != 16 {
		t.Fatalf("unexpected embedding shape: %#v", cfg.Embedding)
	}
	if cfg.Embedding.OpenAIBaseURL != "https://provider.example/v1" || cfg.Embedding.OpenAIKey != "runtime-secret" {
		t.Fatalf("unexpected OpenAI-compatible config: %#v", cfg.Embedding)
	}
}
