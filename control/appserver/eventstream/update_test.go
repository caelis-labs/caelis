package eventstream

import (
	"encoding/json"
	"math"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestContentChunkRoundTripPreservesACPMetadata(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(ContentChunk{
		SessionUpdate: UpdateAgentMessage,
		MessageID:     "msg-1",
		Content:       TextContent{Type: "text", Text: "hello"},
		Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal(ContentChunk) error = %v", err)
	}
	var decoded ContentChunk
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(ContentChunk) error = %v", err)
	}
	if decoded.MessageID != "msg-1" {
		t.Fatalf("message id = %q, want msg-1", decoded.MessageID)
	}
	vendor, _ := decoded.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("meta = %#v, want vendor trace", decoded.Meta)
	}
}

func TestToolCallUpdateRoundTripPreservesContentPresence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		content     []ToolCallContent
		wantPresent bool
	}{
		{name: "omitted"},
		{name: "explicit empty", content: []ToolCallContent{}, wantPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(ToolCallUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Content:       test.content,
			})
			if err != nil {
				t.Fatalf("json.Marshal(ToolCallUpdate) error = %v", err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatalf("json.Unmarshal(ToolCallUpdate object) error = %v", err)
			}
			if _, present := object["content"]; present != test.wantPresent {
				t.Fatalf("ToolCallUpdate JSON = %s, content presence = %t, want %t", raw, present, test.wantPresent)
			}
			decoded, err := DecodeUpdateJSON(raw)
			if err != nil {
				t.Fatalf("DecodeUpdateJSON() error = %v", err)
			}
			update, ok := decoded.(ToolCallUpdate)
			if !ok {
				t.Fatalf("DecodeUpdateJSON() = %T, want ToolCallUpdate", decoded)
			}
			if present := update.Content != nil; present != test.wantPresent {
				t.Fatalf("decoded content = %#v, presence = %t, want %t", update.Content, present, test.wantPresent)
			}
		})
	}
}

func TestRequestPermissionRoundTripPreservesACPMetadata(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: ToolCallUpdate{
			SessionUpdate: UpdateToolCallInfo,
			ToolCallID:    "call-1",
		},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow_once",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
			Meta: map[string]json.RawMessage{
				"vendor": json.RawMessage(`{"scope":"once"}`),
			},
		}},
		Meta: map[string]any{"vendor": map[string]any{"trace": "abc"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal(RequestPermissionRequest) error = %v", err)
	}
	var decoded RequestPermissionRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(RequestPermissionRequest) error = %v", err)
	}
	vendor, _ := decoded.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("meta = %#v, want vendor trace", decoded.Meta)
	}
	if got := string(decoded.Options[0].Meta["vendor"]); got != `{"scope":"once"}` {
		t.Fatalf("option metadata = %s, want preserved vendor metadata", got)
	}
}

func TestUsageUpdateRoundTripPreservesCostAndMetadata(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(UsageUpdate{
		SessionUpdate: UpdateUsage,
		Size:          200000,
		Used:          42000,
		Cost: &acpsdk.Cost{
			Amount:   0.47,
			Currency: "USD",
			Meta: map[string]json.RawMessage{
				"vendor": json.RawMessage(`{"trace":"cost-abc"}`),
			},
		},
		Meta: map[string]any{"vendor": map[string]any{"trace": "abc"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal(UsageUpdate) error = %v", err)
	}
	var decoded UsageUpdate
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(UsageUpdate) error = %v", err)
	}
	if decoded.SessionUpdate != UpdateUsage || decoded.Size != 200000 || decoded.Used != 42000 {
		t.Fatalf("decoded usage = %#v, want size/used preserved", decoded)
	}
	if decoded.Cost == nil || decoded.Cost.Amount != 0.47 || decoded.Cost.Currency != "USD" || string(decoded.Cost.Meta["vendor"]) != `{"trace":"cost-abc"}` {
		t.Fatalf("decoded cost = %#v, want amount/currency/meta", decoded.Cost)
	}
	vendor, _ := decoded.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("meta = %#v, want vendor trace", decoded.Meta)
	}
}

func TestUsageUpdateMarshalIncludesZeroSize(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(UsageUpdate{
		SessionUpdate: UpdateUsage,
		Used:          42,
	})
	if err != nil {
		t.Fatalf("json.Marshal(UsageUpdate) error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(raw) error = %v", err)
	}
	if _, ok := decoded["size"]; !ok {
		t.Fatalf("usage_update JSON = %s, want required size field present", raw)
	}
}

func TestUsageUpdateRoundTripsFullUint64Range(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(UsageUpdate{
		SessionUpdate: UpdateUsage, Size: math.MaxUint64, Used: math.MaxUint64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded UsageUpdate
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Size != math.MaxUint64 || decoded.Used != math.MaxUint64 {
		t.Fatalf("decoded usage = %#v", decoded)
	}
}

func TestDecodeUpdateJSONPreservesSDKOwnedSessionStateUpdate(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sessionUpdate":"current_mode_update","currentModeId":"review","_meta":{"vendor":{"trace":"abc"}}}`)
	update, err := DecodeUpdateJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	preserved, ok := update.(RawUpdate)
	if !ok {
		t.Fatalf("update = %T, want RawUpdate for SDK-owned member", update)
	}
	if preserved.SessionUpdate != UpdateCurrentMode || string(preserved.Raw) != string(raw) {
		t.Fatalf("preserved update = %#v, want exact SDK-owned wire", preserved)
	}
}
