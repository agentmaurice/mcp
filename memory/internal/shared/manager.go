package shared

import "context"

// Manager defines lifecycle hooks for app components.
type Manager interface {
	Start(ctx context.Context) error
	Stop() error
	Name() string
}
