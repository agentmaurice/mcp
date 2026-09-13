package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestOpenAIProviderEmbedsBatches(t *testing.T) {
	var (
		mu      sync.Mutex
		batches [][]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("unexpected authorization: %q", got)
		}
		var request openAIEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Model != "bge-m3" {
			t.Errorf("unexpected model: %s", request.Model)
		}
		mu.Lock()
		batches = append(batches, request.Input)
		mu.Unlock()

		data := make([]map[string]any, len(request.Input))
		for i := range request.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{float32(i + 1), 0.5}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{
		BaseURL: server.URL + "/v1", APIKey: "secret", Model: "bge-m3", Dimensions: 2, BatchSize: 2,
	})
	embeddings, err := provider.Embed(context.Background(), []string{"one", "two", "three"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(embeddings))
	}
	if !reflect.DeepEqual(batches, [][]string{{"one", "two"}, {"three"}}) {
		t.Fatalf("unexpected batches: %#v", batches)
	}
}

func TestOpenAIProviderRejectsDimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{BaseURL: server.URL, Model: "bge-m3", Dimensions: 2})
	if _, err := provider.Embed(context.Background(), []string{"text"}); err == nil {
		t.Fatal("expected dimension mismatch error")
	}
}
