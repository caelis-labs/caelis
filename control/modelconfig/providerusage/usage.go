// Package providerusage owns provider-neutral subscription usage semantics.
// Provider adapters translate their private account APIs into this model;
// presentation surfaces consume only the normalized projection exposed by
// Control.
package providerusage

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Window describes one provider-enforced rolling usage window.
type Window struct {
	Kind        string
	Label       string
	UsedPercent float64
	Duration    time.Duration
	ResetsAt    time.Time
}

// Limit groups one or more windows that meter the same provider feature.
type Limit struct {
	ID      string
	Name    string
	Windows []Window
}

// Snapshot is one non-secret provider subscription usage observation.
type Snapshot struct {
	Provider   string
	Plan       string
	CapturedAt time.Time
	Limits     []Limit
}

// Reader obtains a provider's current subscription usage.
type Reader interface {
	SubscriptionUsage(context.Context) (Snapshot, error)
}

// Registry routes provider usage queries without exposing provider-specific
// response types to product orchestration or presentation surfaces.
type Registry struct {
	mu      sync.RWMutex
	readers map[string]Reader
}

// NewRegistry constructs a registry from normalized provider names.
func NewRegistry(readers map[string]Reader) *Registry {
	registry := &Registry{readers: make(map[string]Reader, len(readers))}
	for provider, reader := range readers {
		provider = normalizeProvider(provider)
		if provider != "" && reader != nil {
			registry.readers[provider] = reader
		}
	}
	return registry
}

// Query returns found=false when the provider has no subscription-usage
// adapter. Unsupported providers never inherit another provider's account.
func (r *Registry) Query(ctx context.Context, provider string) (Snapshot, bool, error) {
	if r == nil {
		return Snapshot{}, false, nil
	}
	r.mu.RLock()
	reader := r.readers[normalizeProvider(provider)]
	r.mu.RUnlock()
	if reader == nil {
		return Snapshot{}, false, nil
	}
	snapshot, err := reader.SubscriptionUsage(ctx)
	if err != nil {
		return Snapshot{}, true, err
	}
	return CloneSnapshot(snapshot), true, nil
}

// CloneSnapshot returns an independent copy of one usage observation.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	if len(in.Limits) == 0 {
		out.Limits = nil
		return out
	}
	out.Limits = make([]Limit, len(in.Limits))
	for i, limit := range in.Limits {
		out.Limits[i] = limit
		out.Limits[i].Windows = append([]Window(nil), limit.Windows...)
	}
	return out
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
