package chat

import (
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRequestCoverageBindsNormalizedHistory(t *testing.T) {
	for _, scenario := range []string{"invalid_args", "orphan_result", "incomplete_batch"} {
		t.Run(scenario, func(t *testing.T) {
			input := model.NewTextMessage(model.RoleUser, "canonical input")
			call := persistedToolCallEvent("call", "work", map[string]any{})
			result := persistedToolResultEvent("call", "work", map[string]any{}, map[string]any{"error": "invalid arguments"})
			events := []*session.Event{{ID: "input", Seq: 7, Type: session.EventTypeUser, Message: &input}}
			switch scenario {
			case "invalid_args":
				// The model stores malformed arguments in a valid JSON wrapper;
				// history repair still drops the complete invalid batch.
				message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call", Name: "work", Args: `{"input":`}}, "")
				call.Message = &message
				events = append(events, call, result)
			case "orphan_result":
				events = append(events, result)
			case "incomplete_batch":
				events = append(events, call)
			}
			for i, event := range events[1:] {
				event.Seq = uint64(8 + i)
			}
			base, tracker := requestsource.WithTracker(t.Context(), events)
			ctx := agent.NewContext(agent.ContextSpec{Context: base, Events: events})
			req := &model.Request{Messages: messagesFromContext(ctx)}
			if !reflect.DeepEqual(req.Messages, []model.Message{input}) {
				t.Fatalf("normalized messages = %#v, want only canonical input", req.Messages)
			}
			prefix, err := requestsource.Fingerprint(tracker.Events())
			if err != nil {
				t.Fatal(err)
			}
			messages, err := requestsource.Fingerprint(req.Messages)
			if err != nil {
				t.Fatal(err)
			}
			before := model.CloneRequest(req)
			bindRequestSource(ctx, req)
			if req.Source == nil {
				t.Fatal("normalized canonical history has no source binding")
			}
			if req.Source.SourceThroughSeq != session.LastEventSeq(events) || req.Source.SourcePrefixHash != prefix || req.Source.MessagesHash != messages {
				t.Fatalf("incorrect source binding: %+v", req.Source)
			}
			if !reflect.DeepEqual(before.Messages, req.Messages) {
				t.Fatal("binding changed the foreground messages")
			}
		})
	}
}

func TestRequestCoverageBindsOnlyExactTrackerProjection(t *testing.T) {
	message := model.NewTextMessage(model.RoleUser, "canonical input")
	event := &session.Event{ID: "input", Seq: 7, Type: session.EventTypeUser, Message: &message}
	for _, scenario := range []string{"exact", "changed", "origin", "extra", "missing", "no_tracker", "dropped_valid_tools"} {
		t.Run(scenario, func(t *testing.T) {
			events := []*session.Event{event}
			if scenario == "dropped_valid_tools" {
				call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call", Name: "work", Args: "{}"}}, "")
				result := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("call", "work", map[string]any{"ok": true}, false))
				events = append(events,
					&session.Event{ID: "call", Seq: 8, Type: session.EventTypeToolCall, Message: &call},
					&session.Event{ID: "result", Seq: 9, Type: session.EventTypeToolResult, Message: &result},
				)
			}
			base, _ := requestsource.WithTracker(t.Context(), events)
			if scenario == "no_tracker" {
				base = t.Context()
			}
			ctx := agent.NewContext(agent.ContextSpec{Context: base, Events: events})
			req := &model.Request{Messages: messagesFromContext(ctx)}
			switch scenario {
			case "changed":
				req.Messages[0].Parts[0].Text.Text = "changed"
			case "origin":
				req.Messages[0].Origin = &model.MessageOrigin{}
			case "extra":
				req.Messages = append(req.Messages, model.NewTextMessage(model.RoleUser, "extra"))
			case "missing":
				req.Messages = nil
			case "dropped_valid_tools":
				if len(req.Messages) != 3 {
					t.Fatalf("messages = %#v, want complete tool batch", req.Messages)
				}
				req.Messages = req.Messages[:1]
			}
			before := model.CloneRequest(req)
			bindRequestSource(ctx, req)
			if (req.Source != nil) != (scenario == "exact") {
				t.Fatalf("source = %+v, scenario %s", req.Source, scenario)
			}
			if !reflect.DeepEqual(before.Messages, req.Messages) {
				t.Fatal("binding changed the foreground messages")
			}
			if req.Source != nil {
				prefix, _ := requestsource.Fingerprint(events)
				messages, _ := requestsource.Fingerprint(req.Messages)
				if req.Source.SourceThroughSeq != 7 || req.Source.SourcePrefixHash != prefix || req.Source.MessagesHash != messages {
					t.Fatalf("incorrect source binding: %+v", req.Source)
				}
			}
		})
	}
}
