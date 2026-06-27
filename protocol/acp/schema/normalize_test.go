package schema

import (
	"encoding/json"
	"testing"
)

type stringValue string

func (s stringValue) String() string {
	return string(s)
}

func TestNormalizeRawMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  any
		want string
	}{
		{
			name: "map",
			raw:  map[string]any{"stdout": "ok"},
			want: "ok",
		},
		{
			name: "raw message object",
			raw:  json.RawMessage(`{"stdout":"ok"}`),
			want: "ok",
		},
		{
			name: "raw message string",
			raw:  json.RawMessage(`not-json`),
			want: "not-json",
		},
		{
			name: "content text",
			raw:  map[string]any{"type": "text", "text": "ok"},
			want: "ok",
		},
		{
			name: "stringer",
			raw:  stringValue("ok"),
			want: "ok",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeRawMap(tt.raw)
			if got["stdout"] == tt.want || got["text"] == tt.want {
				return
			}
			t.Fatalf("NormalizeRawMap(%T) = %#v, want value %q", tt.raw, got, tt.want)
		})
	}
}

func TestExtractTextValue(t *testing.T) {
	t.Parallel()

	got := ExtractTextValue([]any{
		map[string]any{"content": map[string]any{"type": "text", "text": "hello "}},
		map[string]any{"detailedContent": "world"},
	})
	if got != "hello world" {
		t.Fatalf("ExtractTextValue = %q, want nested text", got)
	}
}

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

func TestRequestPermissionRoundTripPreservesACPMetadata(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: ToolCallUpdate{
			SessionUpdate: UpdateToolCallInfo,
			ToolCallID:    "call-1",
		},
		Options: []PermissionOption{{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
		Meta:    map[string]any{"vendor": map[string]any{"trace": "abc"}},
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
}

func TestUsageUpdateRoundTripPreservesCostAndMetadata(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(UsageUpdate{
		SessionUpdate: UpdateUsage,
		Size:          200000,
		Used:          42000,
		Cost: &UsageCost{
			Input:     0.12,
			Output:    0.34,
			CacheRead: 0.01,
			Total:     0.47,
			Currency:  "USD",
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
	if decoded.Cost == nil || decoded.Cost.Total != 0.47 || decoded.Cost.Currency != "USD" {
		t.Fatalf("decoded cost = %#v, want total/currency", decoded.Cost)
	}
	vendor, _ := decoded.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("meta = %#v, want vendor trace", decoded.Meta)
	}
}
