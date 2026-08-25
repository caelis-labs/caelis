package controller

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestACPEnvelopeFromUpdatePassesThroughStandardToolLifecycle(t *testing.T) {
	t.Parallel()

	completed := schema.ToolStatusCompleted
	updates := []client.Update{
		client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "read-1",
			Title:         "Read `AGENTS.md`",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"target_file": "AGENTS.md"},
			Content: []client.ToolCallContent{{
				Type:    "content",
				Content: map[string]any{"type": "text", "text": "reading"},
			}},
		},
		client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "read-1",
			Status:        &completed,
		},
	}
	participant := &acpEnvelopeParticipantScope{
		binding: session.ParticipantBinding{ID: "grok-1", Label: "@grok"},
		agent:   "grok",
		turnID:  "participant-turn-1",
	}
	for i, update := range updates {
		env := acpEnvelopeFromUpdate(client.UpdateEnvelope{
			SessionID: "grok-session",
			Update:    update,
		}, nil, participant)
		if env == nil {
			t.Fatalf("tool lifecycle update %d produced no participant envelope", i)
		}
		if env.Scope != eventstream.ScopeParticipant || env.ParticipantID != "grok-1" || env.TurnID != "participant-turn-1" {
			t.Fatalf("participant envelope scope = %#v", env)
		}
		if !reflect.DeepEqual(env.Update, update) {
			t.Fatalf("participant tool update %d = %#v, want %#v", i, env.Update, update)
		}
	}
}

func TestACPEnvelopeFromUpdatePassesThroughUsageUpdate(t *testing.T) {
	t.Parallel()

	env := acpEnvelopeFromUpdate(client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.UsageUpdate{
			SessionUpdate: client.UpdateUsage,
			Size:          200000,
			Used:          42000,
			Cost:          &client.UsageCost{Total: 0.47, Currency: "USD"},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}, nil, nil)
	if env == nil {
		t.Fatal("acpEnvelopeFromUpdate() = nil, want usage_update envelope")
	}
	update, ok := env.Update.(schema.UsageUpdate)
	if !ok {
		t.Fatalf("Update = %T, want schema.UsageUpdate", env.Update)
	}
	if update.Size != 200000 || update.Used != 42000 {
		t.Fatalf("usage update = %#v, want size/used preserved", update)
	}
	if update.Cost == nil || update.Cost.Total != 0.47 || update.Cost.Currency != "USD" {
		t.Fatalf("usage cost = %#v, want total/currency", update.Cost)
	}
}
