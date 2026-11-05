package runtime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestContextUsageProjectionMatchesFullHistoryAtEveryCut(t *testing.T) {
	message := func(role model.Role, parts ...model.Part) *model.Message {
		value := model.NewMessage(role, parts...)
		return &value
	}
	events := []*session.Event{
		{ID: "user", Type: session.EventTypeUser, Message: message(model.RoleUser, model.NewTextPart("中文 input"), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{URI: "fixture://image"}, "image/png", "image"))},
		{ID: "call", Type: session.EventTypeToolCall, MessageID: "response", Message: message(model.RoleAssistant, model.NewToolUsePart("call", "read", json.RawMessage(`{"limit":20}`))), Invocation: &session.EventInvocation{Provider: "fixture", Model: "one", PromptPrefixTokens: 70}, Meta: map[string]any{"prompt_tokens": 90, "total_tokens": 100, "completion_tokens": 10}},
		{ID: "result", Type: session.EventTypeToolResult, Message: message(model.RoleTool, model.NewToolResultJSONPart("call", "read", map[string]any{"text": strings.Repeat("payload 中", 500)}, false))},
		{ID: "other", Type: session.EventTypeAssistant, Message: message(model.RoleAssistant, model.NewTextPart("other model output")), Invocation: &session.EventInvocation{Provider: "fixture", Model: "two"}, Meta: map[string]any{"prompt_tokens": 300, "total_tokens": 320, "completion_tokens": 20}},
		session.NewModelInvocationReceipt(model.Invocation{ID: "receipt", Provider: "fixture", Model: "one", Usage: model.Usage{PromptTokens: 7000, CompletionTokens: 30, TotalTokens: 7030}}, "main"),
		{ID: "compact", Type: session.EventTypeCompact, Text: "checkpoint replacement", Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{ContractVersion: 1, SummarizedThroughSeq: 2})}},
		{ID: "next-compact", Type: session.EventTypeCompact, Text: "new checkpoint", Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{ContractVersion: 1, SummarizedThroughSeq: 4})}},
		{ID: "next-user", Type: session.EventTypeUser, Message: message(model.RoleUser, model.NewTextPart("new input"))},
	}
	var projection ContextUsageProjection
	for index, event := range events {
		event.Seq = uint64(index + 1)
		projection.Append([]*session.Event{event})
		projection.Append(events[:index+1]) // A repeated catch-up must not recount.
		var canonical []*session.Event
		for _, original := range events[:index+1] {
			if session.IsCanonicalHistoryEvent(original) {
				canonical = append(canonical, original)
			}
		}
		for _, name := range []string{"one", "two", "unobserved"} {
			cfg := CompactionConfig{EstimatedPromptPrefixTokens: 11}
			want := ComputeUsageSnapshotForModel(canonical, nil, 128000, cfg, "fixture", name)
			if got := projection.Snapshot(128000, cfg, "fixture", name); !reflect.DeepEqual(got, want) {
				t.Fatalf("cut=%d model=%s projected=%+v full=%+v", event.Seq, name, got, want)
			}
		}
	}
	for _, event := range projection.events {
		if event.Message != nil || event.Tool != nil {
			t.Fatal("context projection retained transcript payload")
		}
	}
	before := projection.Snapshot(128000, CompactionConfig{}, "fixture", "one")
	cloned := projection.Clone()
	cloned.Append([]*session.Event{{ID: "later", Seq: 9, Type: session.EventTypeUser, Text: "later context"}})
	if got := projection.Snapshot(128000, CompactionConfig{}, "fixture", "one"); got != before {
		t.Fatal("published projection changed while a clone caught up")
	}
}
