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

func TestFullTextSearchSettingsDefaultsAndEnvironment(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Search.FTSStemmer != "porter" || cfg.Search.FTSStopwords != "none" {
		t.Fatalf("FTS defaults = %q/%q, want porter/none", cfg.Search.FTSStemmer, cfg.Search.FTSStopwords)
	}

	viper.Reset()
	t.Setenv("BRAIN_SEARCH_FTS_STEMMER", "french")
	t.Setenv("BRAIN_SEARCH_FTS_STOPWORDS", "english")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load from environment: %v", err)
	}
	if cfg.Search.FTSStemmer != "french" || cfg.Search.FTSStopwords != "english" {
		t.Fatalf("FTS environment not applied: %q/%q", cfg.Search.FTSStemmer, cfg.Search.FTSStopwords)
	}
}

func TestLoadRejectsUnsupportedFullTextStemmer(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("BRAIN_SEARCH_FTS_STEMMER", "klingon")
	if _, err := Load(); err == nil {
		t.Fatal("unsupported stemmer must fail at startup")
	}
}

func TestValidateFullTextSettings(t *testing.T) {
	valid := [][2]string{{"porter", "none"}, {"french", "english"}, {"none", "stop_words_fr"}}
	for _, v := range valid {
		if err := ValidateFullTextSettings(v[0], v[1]); err != nil {
			t.Fatalf("ValidateFullTextSettings(%q, %q) = %v, want nil", v[0], v[1], err)
		}
	}
	invalid := [][2]string{{"porter", "a b"}, {"porter", "x'; DROP"}, {"", "none"}, {"Porter", "none"}}
	for _, v := range invalid {
		if err := ValidateFullTextSettings(v[0], v[1]); err == nil {
			t.Fatalf("ValidateFullTextSettings(%q, %q) must fail", v[0], v[1])
		}
	}
}

func TestDuckDBExtensionDirFromEnvironment(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Storage.DuckDBExtensionDir != "" {
		t.Fatalf("default extension dir must be empty, got %q", cfg.Storage.DuckDBExtensionDir)
	}

	viper.Reset()
	t.Setenv("BRAIN_DUCKDB_EXTENSION_DIR", "/opt/brain/duckdb-extensions")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load from environment: %v", err)
	}
	if cfg.Storage.DuckDBExtensionDir != "/opt/brain/duckdb-extensions" {
		t.Fatalf("BRAIN_DUCKDB_EXTENSION_DIR not applied: %q", cfg.Storage.DuckDBExtensionDir)
	}
}
