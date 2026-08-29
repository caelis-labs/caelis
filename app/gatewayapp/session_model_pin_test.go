package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
)

func TestSessionModelPinRegistryRejectsStaleCredentialGeneration(t *testing.T) {
	ctx := context.Background()
	currentSecret := "old-secret"
	resolveAPIKey := func(context.Context, string) (string, error) {
		return currentSecret, nil
	}
	oldConfig := normalizeModelConfig(ModelConfig{
		Provider:      "openai",
		API:           providers.APIOpenAI,
		Model:         "credential-generation-model",
		BaseURL:       "https://api.openai.com/v1",
		CredentialRef: "apikey:openai@default",
		Token:         "old-secret",
	})
	canonical := oldConfig
	canonical.Token = ""
	registry := newSessionModelPinRegistry(resolveAPIKey, canonical)
	release, err := registry.retain(ctx, "existing-child", oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)

	newConfig := oldConfig
	newConfig.Token = "new-secret"
	currentSecret = newConfig.Token
	registry.syncConfiguredModels([]ModelConfig{canonical})
	if _, ok := registry.config(ctx, "existing-child"); ok {
		t.Fatal("credential replacement retained the old child Session pin")
	}
	if _, err := registry.retain(ctx, "stale-child", oldConfig); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("retain(stale credential generation) error = %v, want not configured", err)
	}
	currentRelease, err := registry.retain(ctx, "current-child", newConfig)
	if err != nil {
		t.Fatalf("retain(current credential generation): %v", err)
	}
	currentRelease()
}
