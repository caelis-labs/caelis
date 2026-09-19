package acp

import (
	"encoding/json"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSessionNotificationForWirePreservesToolNamesV1(t *testing.T) {
	t.Parallel()
	for _, updateType := range []string{eventstream.UpdateToolCall, eventstream.UpdateToolCallInfo} {
		for _, field := range []string{"", `,"name":null`, `,"name":"ReadFile"`, `,"name":""`} {
			t.Run(updateType+field, func(t *testing.T) {
				raw := []byte(`{"sessionId":"s","update":{"sessionUpdate":"` + updateType + `","toolCallId":"c","title":"Inspect file"` + field + `}}`)
				var input struct {
					Update json.RawMessage `json:"update"`
				}
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				update, err := eventstream.DecodeUpdateJSON(input.Update)
				if err != nil {
					t.Fatal(err)
				}
				notification := eventstream.SessionNotification{SessionID: "s", Update: update}
				wire, err := sessionNotificationForWire(notification)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				var want, got struct {
					Update struct {
						Name *string `json:"name"`
					} `json:"update"`
				}
				if err := json.Unmarshal(raw, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("wire = %s, want name from %s", encoded, raw)
				}
			})
		}
	}
}

func TestSessionNotificationForWireUsesSDKForStandardUpdate(t *testing.T) {
	notification := eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "hello"},
			MessageID:     "message-1",
			Meta:          map[string]any{"trace": "kept"},
		},
	}

	wireValue, err := sessionNotificationForWire(notification)
	if err != nil {
		t.Fatal(err)
	}
	wire, ok := wireValue.(acpsdk.SessionNotification)
	if !ok {
		t.Fatalf("wire = %T, want acpsdk.SessionNotification", wireValue)
	}
	update := wire.Update.AgentMessageChunk
	if wire.SessionId != "session-1" || update == nil || update.Content.Text == nil || update.Content.Text.Text != "hello" {
		t.Fatalf("SDK notification = %#v", wire)
	}
	if update.MessageId == nil || *update.MessageId != "message-1" || string(update.Meta["trace"]) != `"kept"` {
		t.Fatalf("SDK update = %#v", update)
	}
}

func TestSessionNotificationForWireRejectsMalformedStandardUpdate(t *testing.T) {
	_, err := sessionNotificationForWire(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       map[string]any{"type": "text"},
		},
	})
	if err == nil {
		t.Fatal("sessionNotificationForWire() error = nil")
	}
}

func TestSessionNotificationForWirePreservesExtensionContent(t *testing.T) {
	notification := eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Content: []eventstream.ToolCallContent{{
				Type: "vendor", Content: map[string]any{"value": "kept"},
			}},
		},
	}
	wireValue, err := sessionNotificationForWire(notification)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wireValue.(eventstream.SessionNotification); !ok {
		t.Fatalf("wire = %T, want compatibility notification", wireValue)
	}
}

func TestSessionNotificationForWireDoesNotLetExtensionHideMalformedStandardContent(t *testing.T) {
	tests := map[string]eventstream.ToolCallContent{
		"text": {
			Type: "content", Content: map[string]any{"type": "text"},
		},
		"diff": {
			Type: "diff", NewText: "after",
		},
		"terminal": {
			Type: "terminal",
		},
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := sessionNotificationForWire(eventstream.SessionNotification{
				SessionID: "session-1",
				Update: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Content: []eventstream.ToolCallContent{
						{Type: "vendor", Content: map[string]any{"value": "kept"}},
						malformed,
					},
				},
			})
			if err == nil {
				t.Fatal("sessionNotificationForWire() error = nil")
			}
		})
	}
}

func TestSessionNotificationForWirePreservesSessionInfoNull(t *testing.T) {
	notification := eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.RawUpdate{
			SessionUpdate: eventstream.UpdateSessionInfo,
			Raw:           json.RawMessage(`{"sessionUpdate":"session_info_update","title":null,"updatedAt":null}`),
		},
	}
	wireValue, err := sessionNotificationForWire(notification)
	if err != nil {
		t.Fatal(err)
	}
	wire, ok := wireValue.(acpsdk.SessionNotification)
	if !ok {
		t.Fatalf("wire = %T, want acpsdk.SessionNotification", wireValue)
	}
	update := wire.Update.SessionInfoUpdate
	if update == nil || update.TitleState() != acpsdk.NullableFieldNull || update.UpdatedAtState() != acpsdk.NullableFieldNull {
		t.Fatalf("SDK session info update = %#v", update)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update","title":null,"updatedAt":null}}` {
		t.Fatalf("session info wire = %s", encoded)
	}
}

func TestSessionNotificationForWireUsesSDKForStandardUsageCost(t *testing.T) {
	notification := eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage,
			Size:          200_000,
			Used:          42_000,
			Cost:          &acpsdk.Cost{Amount: 0.3, Currency: "USD"},
		},
	}
	wireValue, err := sessionNotificationForWire(notification)
	if err != nil {
		t.Fatal(err)
	}
	wire, ok := wireValue.(acpsdk.SessionNotification)
	if !ok {
		t.Fatalf("wire = %T, want SDK notification", wireValue)
	}
	update := wire.Update.UsageUpdate
	if update == nil || update.Cost == nil || update.Cost.Amount != 0.3 || update.Cost.Currency != "USD" {
		t.Fatalf("usage wire = %#v", wire)
	}
}

func TestSessionNotificationForWireUsesSDKForUsageWithoutCost(t *testing.T) {
	wireValue, err := sessionNotificationForWire(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage,
			Size:          200_000,
			Used:          42_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, ok := wireValue.(acpsdk.SessionNotification)
	if !ok || wire.Update.UsageUpdate == nil || wire.Update.UsageUpdate.Size != 200_000 || wire.Update.UsageUpdate.Used != 42_000 {
		t.Fatalf("SDK usage wire = %#v", wireValue)
	}
}

func TestStandardSessionUpdateTypeMatchesSDKUnion(t *testing.T) {
	for _, updateType := range []string{
		eventstream.UpdateUserMessage,
		eventstream.UpdateAgentMessage,
		eventstream.UpdateAgentThought,
		eventstream.UpdateToolCall,
		eventstream.UpdateToolCallInfo,
		eventstream.UpdatePlan,
		eventstream.UpdateAvailableCmds,
		eventstream.UpdateCurrentMode,
		eventstream.UpdateConfigOption,
		eventstream.UpdateSessionInfo,
		eventstream.UpdateUsage,
	} {
		if !standardSessionUpdateType(updateType) {
			t.Fatalf("standardSessionUpdateType(%q) = false", updateType)
		}
	}
	for _, updateType := range []string{"", eventstream.UpdateCompact, "vendor_update"} {
		if standardSessionUpdateType(updateType) {
			t.Fatalf("standardSessionUpdateType(%q) = true", updateType)
		}
	}
}

func TestSessionNotificationForWirePreservesExtensionUpdate(t *testing.T) {
	tests := map[string]struct {
		update eventstream.Update
		want   string
	}{
		"compact": {
			update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateCompact,
				Content:       eventstream.TextContent{Type: "text", Text: "Compacted"},
			},
			want: `{"sessionId":"session-1","update":{"sessionUpdate":"compact","content":{"type":"text","text":"Compacted"}}}`,
		},
		"unknown vendor update": {
			update: eventstream.RawUpdate{
				SessionUpdate: "vendor_update",
				Raw:           json.RawMessage(`{"sessionUpdate":"vendor_update","value":{"nested":true}}`),
			},
			want: `{"sessionId":"session-1","update":{"sessionUpdate":"vendor_update","value":{"nested":true}}}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			wireValue, err := sessionNotificationForWire(eventstream.SessionNotification{
				SessionID: "session-1",
				Update:    test.update,
			})
			if err != nil {
				t.Fatal(err)
			}
			wire, ok := wireValue.(eventstream.SessionNotification)
			if !ok {
				t.Fatalf("wire = %T, want eventstream.SessionNotification", wireValue)
			}
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("extension wire = %s, want %s", encoded, test.want)
			}
		})
	}
}

func TestSessionNotificationForWireRejectsMissingUpdate(t *testing.T) {
	if _, err := sessionNotificationForWire(eventstream.SessionNotification{SessionID: "session-1"}); err == nil {
		t.Fatal("sessionNotificationForWire() error = nil")
	}
}
