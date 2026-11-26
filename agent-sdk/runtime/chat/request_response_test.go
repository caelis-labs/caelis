package chat

import (
	"context"
	"errors"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"iter"
	"reflect"
	"testing"
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

func TestRequestCoverageRejectsUnverifiedSources(t *testing.T) {
	t.Run("unprojectable history", func(t *testing.T) {
		first := persistedToolCallEvent("call", "work", nil)
		second := session.CloneEvent(first)
		first.ID, first.Seq = "first", 1
		second.ID, second.Seq = "second", 2
		claim := tool.InvocationContext{SessionID: "session", TurnID: "turn", ItemID: "same-occurrence"}
		first.Tool.Execution = claim
		second.Tool.Execution = claim
		events := []*session.Event{first, second}
		if _, err := session.OrderToolFacts(events); err == nil {
			t.Fatal("fixture must fail history projection")
		}
		base, _ := requestsource.WithTracker(t.Context(), events)
		ctx := agent.NewContext(agent.ContextSpec{Context: base})
		req := &model.Request{}
		bindRequestSource(ctx, req)
		if req.Source != nil {
			t.Fatalf("unprojectable tool history bound as empty messages: %+v", req.Source)
		}
	})

	t.Run("unverified prefilled source", func(t *testing.T) {
		ctx := agent.NewContext(agent.ContextSpec{Context: t.Context()})
		req := &model.Request{Source: &model.RequestSource{SourceThroughSeq: 1}}
		bindRequestSource(ctx, req)
		if req.Source != nil {
			t.Fatalf("source survived without an observed history: %+v", req.Source)
		}
	})
}

func TestFactoryRebuildsRequestsFromNewConfigurationWithoutChangingActiveSnapshot(t *testing.T) {
	llm := &recordingModel{}
	stream := false
	spec := agent.AgentSpec{Name: "main", Model: llm, Metadata: map[string]any{"system_prompt": "original instructions", "reasoning_effort": "low"}, Request: agent.ModelRequestOptions{Stream: &stream, ServiceTier: model.ServiceTierPriority}}
	factory := Factory{}
	active, err := factory.NewAgent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	// Configuration is mutable outside the assembled Agent. A later assembly
	// samples it; the already assembled Agent must retain its detached snapshot.
	stream = true
	spec.Metadata["system_prompt"] = "updated instructions"
	spec.Metadata["reasoning_effort"] = "high"
	spec.Request.ServiceTier = ""
	run := func(a agent.Agent) model.Request {
		t.Helper()
		for _, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: context.Background()})) {
			if err != nil {
				t.Fatal(err)
			}
		}
		return llm.last
	}
	old := run(active)
	if old.Stream || old.ServiceTier != model.ServiceTierPriority || old.Reasoning.Effort != "low" || len(old.Instructions) != 1 || old.Instructions[0].Text.Text != "original instructions" {
		t.Fatalf("active request changed: %#v", old)
	}
	next, err := factory.NewAgent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	updated := run(next)
	if !updated.Stream || updated.ServiceTier != "" || updated.Reasoning.Effort != "high" || len(updated.Instructions) != 1 || updated.Instructions[0].Text.Text != "updated instructions" {
		t.Fatalf("new request retained old configuration: %#v", updated)
	}
}

type accountingGapModel struct{ calls int }

func (*accountingGapModel) Name() string { return "accounting-gap" }
func (m *accountingGapModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	message := model.NewTextMessage(model.RoleAssistant, "done")
	if m.calls == 1 {
		message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{Name: "echo", Args: "{}"}}, "")
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, Provider: "probe", Model: m.Name(), Usage: model.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}), nil)
	}
}
func TestRepairRetainsEveryProviderInvocation(t *testing.T) {
	llm := &accountingGapModel{}
	a, err := New("main", llm, "")
	if err != nil {
		t.Fatal(err)
	}
	recorded, tokens := 0, 0
	var events []*session.Event
	for event, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: context.Background()})) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	for _, event := range session.InvocationAccountingEvents(events) {
		if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
			recorded++
			tokens += usage.TotalTokens
		}
	}
	if llm.calls != 2 || recorded != 2 || tokens != 24 {
		t.Fatalf("actual calls=%d; recorded usage events=%d; total tokens=%d; want 2/2/24", llm.calls, recorded, tokens)
	}
}

func TestResponseAndReceiptUseSameCacheAccountingMetadata(t *testing.T) {
	for _, basis := range []model.CacheReadAccounting{model.CacheReadIncluded, model.CacheReadSeparate, ""} {
		t.Run(string(basis), func(t *testing.T) {
			usage := model.Usage{
				PromptTokens: 10, CachedInputTokens: 5, CompletionTokens: 2, TotalTokens: 17,
				CacheReadAccounting: basis,
			}
			response := modelResponseEvent(model.NewTextMessage(model.RoleAssistant, "done"),
				&model.Response{Provider: "deepseek", Usage: usage}, "message", nil)
			receipt := session.NewModelInvocationReceipt(model.Invocation{Provider: "deepseek", Usage: usage}, "chat")
			sdkUsage := func(event *session.Event) any {
				return event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["usage"]
			}
			if displayed, recorded := sdkUsage(response), sdkUsage(receipt); !reflect.DeepEqual(displayed, recorded) {
				t.Fatalf("response usage = %#v, want receipt usage %#v", displayed, recorded)
			}
		})
	}
}

type cancellationProbeModel struct {
	calls   int
	cancel  context.CancelFunc
	yielded int
}

func (*cancellationProbeModel) Name() string { return "cancellation-probe" }
func (m *cancellationProbeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.cancel != nil {
			m.cancel()
		}
		for range 3 {
			m.yielded++
			if !yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "late success"), TurnComplete: true}), nil) {
				return
			}
		}
	}
}

func TestChatRunRejectsProviderSuccessAfterCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_request", false: "during_stream"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &cancellationProbeModel{}
			if before {
				cancel()
			} else {
				llm.cancel = cancel
			}
			a, err := New("main", llm, "")
			if err != nil {
				t.Fatal(err)
			}
			var runErr error
			for event, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: ctx})) {
				if event != nil && session.EventTypeOf(event) == session.EventTypeAssistant {
					t.Errorf("cancelled request emitted assistant: %#v", event)
				}
				if err != nil {
					runErr = err
				}
			}
			if !errors.Is(runErr, context.Canceled) {
				t.Errorf("error = %v, want cancellation", runErr)
			}
			if before && llm.calls != 0 {
				t.Errorf("provider calls = %d, want zero", llm.calls)
			}
			if !before && llm.yielded != 1 {
				t.Errorf("stream consumed %d events, want stop at first cancelled yield", llm.yielded)
			}
		})
	}
}
