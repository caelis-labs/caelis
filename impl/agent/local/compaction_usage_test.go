package local

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestComputeUsageSnapshotIncludesEstimatedPromptPrefix(t *testing.T) {
	msg := model.NewTextMessage(model.RoleUser, "hello")
	events := []*session.Event{{
		ID:         "u1",
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
	}}

	got := ComputeUsageSnapshot(events, nil, 1000, CompactionConfig{
		EstimatedPromptPrefixTokens: 400,
	})

	if got.Source != compact.UsageSourceEstimated {
		t.Fatalf("usage source = %q, want estimated", got.Source)
	}
	if got.EstimatedPrefixTokens != 400 {
		t.Fatalf("estimated prefix = %d, want 400", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens <= 400 {
		t.Fatalf("total tokens = %d, want prompt text plus estimated prefix", got.TotalTokens)
	}
}

func TestComputeUsageSnapshotDoesNotDoubleCountPrefixWithProviderBaseline(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "world")
	events := []*session.Event{
		{
			ID:         "u1",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &user,
			Text:       user.TextContent(),
		},
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Text:       assistant.TextContent(),
			Meta: map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 5,
				"total_tokens":      105,
			},
		},
	}

	got := ComputeUsageSnapshot(events, nil, 1000, CompactionConfig{
		EstimatedPromptPrefixTokens: 400,
	})

	if got.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider", got.Source)
	}
	if got.EstimatedPrefixTokens != 0 {
		t.Fatalf("estimated prefix = %d, want 0 when provider baseline exists", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens >= 400 {
		t.Fatalf("total tokens = %d, provider baseline should already include prompt prefix", got.TotalTokens)
	}
}

func TestComputeUsageSnapshotIncludesAnthropicCachedInputBaseline(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	events := []*session.Event{
		{
			ID:         "u1",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &user,
			Text:       user.TextContent(),
		},
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Text:       assistant.TextContent(),
			Meta: map[string]any{
				"caelis": map[string]any{
					"sdk": map[string]any{
						"provider": "deepseek",
						"model":    "deepseek-v4-flash",
						"usage": map[string]any{
							"provider":            "deepseek-anthropic",
							"prompt_tokens":       94,
							"cached_input_tokens": 11008,
							"completion_tokens":   194,
							"total_tokens":        288,
						},
					},
				},
			},
		},
	}

	got := ComputeUsageSnapshot(events, nil, 1048576, CompactionConfig{})

	if got.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider", got.Source)
	}
	if got.TotalTokens < 11102 {
		t.Fatalf("total tokens = %d, want provider baseline to include cached input", got.TotalTokens)
	}
}
