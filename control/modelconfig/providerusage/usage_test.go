package providerusage

import (
	"context"
	"testing"
	"time"
)

type testReader struct {
	snapshot Snapshot
}

func (r testReader) SubscriptionUsage(context.Context) (Snapshot, error) {
	return r.snapshot, nil
}

func TestRegistryRoutesByNormalizedProviderAndClones(t *testing.T) {
	registry := NewRegistry(map[string]Reader{
		"OpenAI-Codex": testReader{snapshot: Snapshot{
			Provider: "openai-codex",
			Limits:   []Limit{{ID: "codex", Windows: []Window{{Duration: 5 * time.Hour}}}},
		}},
	})

	got, found, err := registry.Query(context.Background(), " openai-CODEX ")
	if err != nil || !found {
		t.Fatalf("Query() = %#v, %v, %v", got, found, err)
	}
	got.Limits[0].Windows[0].Duration = time.Hour
	again, _, err := registry.Query(context.Background(), "openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	if again.Limits[0].Windows[0].Duration != 5*time.Hour {
		t.Fatalf("registry returned aliased snapshot: %#v", again)
	}
}

func TestRegistryUnsupportedProviderIsAbsent(t *testing.T) {
	registry := NewRegistry(nil)
	if got, found, err := registry.Query(context.Background(), "gemini"); err != nil || found || len(got.Limits) != 0 {
		t.Fatalf("Query() = %#v, %v, %v; want absent", got, found, err)
	}
}
