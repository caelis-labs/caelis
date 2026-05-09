package local

import (
	"testing"

	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestComputeUsageSnapshotIncludesEstimatedPromptPrefix(t *testing.T) {
	msg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")
	events := []*sdksession.Event{{
		ID:         "u1",
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
	}}

	got := ComputeUsageSnapshot(events, nil, 1000, CompactionConfig{
		EstimatedPromptPrefixTokens: 400,
	})

	if got.Source != sdkcompact.UsageSourceEstimated {
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
	user := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")
	assistant := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "world")
	events := []*sdksession.Event{
		{
			ID:         "u1",
			Type:       sdksession.EventTypeUser,
			Visibility: sdksession.VisibilityCanonical,
			Message:    &user,
			Text:       user.TextContent(),
		},
		{
			ID:         "a1",
			Type:       sdksession.EventTypeAssistant,
			Visibility: sdksession.VisibilityCanonical,
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

	if got.Source != sdkcompact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider", got.Source)
	}
	if got.EstimatedPrefixTokens != 0 {
		t.Fatalf("estimated prefix = %d, want 0 when provider baseline exists", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens >= 400 {
		t.Fatalf("total tokens = %d, provider baseline should already include prompt prefix", got.TotalTokens)
	}
}
