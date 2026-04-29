//go:build e2e

package providers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	"github.com/OnslaughtSnail/caelis/sdk/model/providers/e2etest"
)

func TestProviderE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       512,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var final *sdkmodel.Response
	for event, err := range spec.LLM.Generate(ctx, &sdkmodel.Request{
		Instructions: []sdkmodel.Part{
			sdkmodel.NewTextPart("Answer tersely."),
		},
		Messages: []sdkmodel.Message{
			sdkmodel.NewTextMessage(sdkmodel.RoleUser, "Reply with exactly: SDK provider e2e ok"),
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}

	if final == nil {
		t.Fatal("expected final response")
	}
	if got := strings.TrimSpace(final.Message.TextContent()); got == "" {
		t.Fatal("expected non-empty assistant text")
	}
}
