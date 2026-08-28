package controller

import (
	"encoding/json"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
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

func TestACPEnvelopeFromUpdateRestoresGrokExecutePresentationForSideParticipant(t *testing.T) {
	t.Parallel()

	env := acpEnvelopeFromUpdate(client.UpdateEnvelope{
		SessionID: "grok-session",
		Update: client.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "execute-1",
			Title:         "run_terminal_command",
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "git status --short"},
			Meta: map[string]any{"x.ai/tool": map[string]any{
				"version": 1, "name": "run_terminal_command", "kind": "execute",
				"namespace": "grok_build", "label": "Run Command", "read_only": false,
			}},
		},
	}, nil, &acpEnvelopeParticipantScope{
		binding: session.ParticipantBinding{ID: "grok-1", Label: "@grok"},
		agent:   "grok",
		turnID:  "participant-turn-1",
	})
	if env == nil || env.Scope != eventstream.ScopeParticipant || env.ParticipantID != "grok-1" {
		t.Fatalf("participant envelope = %#v", env)
	}
	call, ok := env.Update.(schema.ToolCall)
	if !ok || call.Kind != schema.ToolKindExecute || call.Title != "run_terminal_command" {
		t.Fatalf("participant Grok execute update = %#v, want anonymous standard execute presentation", env.Update)
	}
	if exactName := metautil.String(call.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); exactName != "" {
		t.Fatalf("participant runtime exact tool name = %q, want none", exactName)
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
			Cost: &acpsdk.Cost{
				Amount:   0.47,
				Currency: "USD",
				Meta:     map[string]json.RawMessage{"vendor": json.RawMessage(`{"trace":"cost-abc"}`)},
			},
			Meta: map[string]any{"vendor": map[string]any{"trace": "abc"}},
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
	if update.Cost == nil || update.Cost.Amount != 0.47 || update.Cost.Currency != "USD" || string(update.Cost.Meta["vendor"]) != `{"trace":"cost-abc"}` {
		t.Fatalf("usage cost = %#v, want amount/currency/meta", update.Cost)
	}
}
