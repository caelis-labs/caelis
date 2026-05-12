//go:build e2e

package providers_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers/e2etest"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

func TestCodeFreeProviderMultiTurnE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CODEFREE_LIVE_E2E")) != "1" {
		t.Skip("set CODEFREE_LIVE_E2E=1 to run live CodeFree multi-turn e2e")
	}
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "codefree",
		DefaultModel:    "GLM-5.1",
		Timeout:         120 * time.Second,
		MaxTokens:       512,
	})
	if spec.Provider != "codefree" {
		t.Skipf("provider = %q, want codefree", spec.Provider)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	firstPrompt := "介绍一下你自己，用一句话回答。"
	first := runStreamingTurnForCodeFreeE2E(t, ctx, spec.LLM, []model.Message{
		model.NewTextMessage(model.RoleUser, firstPrompt),
	})
	if got := strings.TrimSpace(first.Message.TextContent()); got == "" {
		t.Fatal("expected first turn to return non-empty assistant text")
	}

	second := runStreamingTurnForCodeFreeE2E(t, ctx, spec.LLM, []model.Message{
		model.NewTextMessage(model.RoleUser, firstPrompt),
		first.Message,
		model.NewTextMessage(model.RoleUser, "演示一下你的工具调用能力，用一句话回答。"),
	})
	if got := strings.TrimSpace(second.Message.TextContent()); got == "" {
		t.Fatal("expected second turn to return non-empty assistant text")
	}
}

func runStreamingTurnForCodeFreeE2E(t *testing.T, ctx context.Context, llm model.LLM, messages []model.Message) *model.Response {
	t.Helper()

	var final *model.Response
	for event, err := range llm.Generate(ctx, &model.Request{
		Instructions: []model.Part{
			model.NewTextPart("Answer tersely."),
		},
		Messages: messages,
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	return final
}
