package shared

import (
	"context"
	"strings"
)

const DefaultSessionKey = "default"

type sessionContextKey struct{}

// WithSessionKey stores a logical browser session key in the request context.
func WithSessionKey(ctx context.Context, key string) context.Context {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		trimmed = DefaultSessionKey
	}
	return context.WithValue(ctx, sessionContextKey{}, trimmed)
}

// SessionKeyFromContext reads the logical browser session key from context.
func SessionKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultSessionKey
	}
	if value := ctx.Value(sessionContextKey{}); value != nil {
		if key, ok := value.(string); ok {
			key = strings.TrimSpace(key)
			if key != "" {
				return key
			}
		}
	}
	return DefaultSessionKey
}
