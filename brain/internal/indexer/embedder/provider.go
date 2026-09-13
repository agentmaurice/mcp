package embedder

import "context"

// Provider defines the embedding provider interface.
type Provider interface {
	// Embed generates embeddings for the given texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions returns the embedding vector dimensions.
	Dimensions() int
	// Model returns the model name.
	Model() string
}
