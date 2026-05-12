//go:build e2e

package providers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers/e2etest"
	"github.com/OnslaughtSnail/caelis/ports/model"
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

	var final *model.Response
	for event, err := range spec.LLM.Generate(ctx, &model.Request{
		Instructions: []model.Part{
			model.NewTextPart("Answer tersely."),
		},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "Reply with exactly: SDK provider e2e ok"),
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
