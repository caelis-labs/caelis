package chat

import (
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRequestCoverageBindsOnlyExactTrackerProjection(t *testing.T) {
	message := model.NewTextMessage(model.RoleUser, "canonical input")
	event := &session.Event{ID: "input", Seq: 7, Type: session.EventTypeUser, Message: &message}
	for _, scenario := range []string{"exact", "changed", "origin", "extra", "missing", "no_tracker", "dropped_tool"} {
		t.Run(scenario, func(t *testing.T) {
			events := []*session.Event{event}
			if scenario == "dropped_tool" {
				call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "open", Name: "work", Args: "{}"}}, "")
				events = append(events, &session.Event{ID: "call", Seq: 8, Type: session.EventTypeToolCall, Message: &call})
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
