package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/tool"
)

func TestRegressionMultiToolCallOrderingTrace(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("multi-tool",
		evalharness.ToolCallStep("looking...", model.ToolCall{
			ID:   "call-read",
			Name: "READ",
			Args: `{"path":"main.go"}`,
		}, model.ToolCall{
			ID:   "call-search",
			Name: "SEARCH",
			Args: `{"path":".","pattern":"func main"}`,
		}, model.ToolCall{
			ID:   "call-list",
			Name: "LIST",
			Args: `{"path":"."}`,
		}),
		evalharness.TextStep("all done"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "multi-tool",
		SessionID:    "sess-multi-tool",
		Prompt:       "inspect the codebase",
		SystemPrompt: "Use tools when needed.",
		Model:        scripted,
		Tools: []tool.Tool{
			evalharness.EchoTool("READ"),
			evalharness.EchoTool("SEARCH"),
			evalharness.EchoTool("LIST"),
		},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}
	if got, want := len(run.Requests), 2; got != want {
		t.Fatalf("len(Requests) = %d, want %d", got, want)
	}

	trace := evalharness.EventTrace(run.Events)

	callIDs := make([]string, 0)
	resultIDs := make([]string, 0)
	for _, entry := range trace {
		switch entry.Type {
		case "tool_call":
			if entry.Tool != nil {
				callIDs = append(callIDs, entry.Tool.ID)
			}
		case "tool_result":
			if entry.Tool != nil {
				resultIDs = append(resultIDs, entry.Tool.ID)
			}
		}
	}

	wantCallOrder := []string{"call-read", "call-search", "call-list"}
	if len(callIDs) != len(wantCallOrder) {
		t.Fatalf("tool_call count = %d, want %d", len(callIDs), len(wantCallOrder))
	}
	for i, want := range wantCallOrder {
		if callIDs[i] != want {
			t.Fatalf("tool_call[%d] = %q, want %q", i, callIDs[i], want)
		}
	}

	wantResultOrder := []string{"call-read", "call-search", "call-list"}
	if len(resultIDs) != len(wantResultOrder) {
		t.Fatalf("tool_result count = %d, want %d", len(resultIDs), len(wantResultOrder))
	}
	for i, want := range wantResultOrder {
		if resultIDs[i] != want {
			t.Fatalf("tool_result[%d] = %q, want %q", i, resultIDs[i], want)
		}
	}

	firstEvent := trace[0]
	if firstEvent.Text != "looking..." {
		t.Fatalf("first event text = %q, want 'looking...'", firstEvent.Text)
	}
	if len(firstEvent.MessageToolCalls) != 3 {
		t.Fatalf("first event message_tool_calls = %d, want 3", len(firstEvent.MessageToolCalls))
	}

	lastEvent := trace[len(trace)-1]
	if lastEvent.Type != "assistant" || lastEvent.Text != "all done" {
		t.Fatalf("last event = %q %q, want assistant \"all done\"", lastEvent.Type, lastEvent.Text)
	}

	postToolMessages := evalharness.RequestMessagesJSON(run.Requests[1])
	if !strings.Contains(postToolMessages, "looking...") {
		t.Fatal("post-tool request should contain the assistant text 'looking...'")
	}
}

func TestRegressionReasoningTextSeparationTrace(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("reasoning",
		evalharness.AssistantPartsStep("the answer", "internal chain of thought"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "reasoning-separation",
		SessionID:    "sess-reasoning-separation",
		Prompt:       "what is 2+2",
		SystemPrompt: "Think step by step.",
		Model:        scripted,
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}
	if got, want := len(run.Requests), 1; got != want {
		t.Fatalf("len(Requests) = %d, want %d", got, want)
	}

	got := evalharness.StableJSON(evalharness.EventTrace(run.Events))
	want := `[
  {
    "visibility": "canonical",
    "type": "assistant",
    "role": "assistant",
    "text": "the answer",
    "reasoning": "internal chain of thought"
  }
]`
	if got != want {
		t.Fatalf("reasoning/text separation trace mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}

	canonical := evalharness.CanonicalEvents(run.Events)
	for _, event := range canonical {
		if event.Visibility == "ui_only" {
			t.Fatalf("canonical events should not contain ui_only events")
		}
	}
}

func TestRegressionMultiToolParallelExecutionTrace(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("parallel-tools",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-a",
			Name: "ECHO",
			Args: `{"value":"alpha"}`,
		}, model.ToolCall{
			ID:   "call-b",
			Name: "ECHO",
			Args: `{"value":"beta"}`,
		}),
		evalharness.TextStep("both done"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "parallel-tools",
		SessionID:    "sess-parallel-tools",
		Prompt:       "echo both",
		SystemPrompt: "",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}
	if got, want := len(run.Requests), 2; got != want {
		t.Fatalf("len(Requests) = %d, want %d", got, want)
	}

	trace := evalharness.EventTrace(run.Events)
	if len(trace) < 5 {
		t.Fatalf("expected at least 5 events, got %d", len(trace))
	}

	callEvents := 0
	resultEvents := 0
	for _, entry := range trace {
		switch entry.Type {
		case "tool_call":
			callEvents++
		case "tool_result":
			resultEvents++
		}
	}
	if callEvents != 2 {
		t.Fatalf("expected 2 tool_call events, got %d", callEvents)
	}
	if resultEvents != 2 {
		t.Fatalf("expected 2 tool_result events, got %d", resultEvents)
	}

	lastEvent := trace[len(trace)-1]
	if lastEvent.Type != "assistant" || lastEvent.Text != "both done" {
		t.Fatalf("last event = %q %q, want assistant \"both done\"", lastEvent.Type, lastEvent.Text)
	}

	resultIDs := make([]string, 0)
	for _, entry := range trace {
		if entry.Type == "tool_result" && entry.Tool != nil {
			resultIDs = append(resultIDs, entry.Tool.ID)
		}
	}
	if len(resultIDs) == 2 && resultIDs[0] != "call-a" {
		t.Fatalf("first result = %q, want call-a (index order preserved)", resultIDs[0])
	}
}

func TestRegressionInvalidToolCallStaysUIOnly(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("ui-only-invalid",
		evalharness.ToolCallStep("trying...", model.ToolCall{
			ID:   "call-bad",
			Name: "ECHO",
			Args: `{invalid`,
		}),
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-good",
			Name: "ECHO",
			Args: `{"value":"ok"}`,
		}),
		evalharness.TextStep("fixed"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "ui-only-invalid",
		SessionID:    "sess-ui-only-invalid",
		Prompt:       "do something",
		SystemPrompt: "",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	canonical := evalharness.CanonicalEvents(run.Events)
	for _, event := range canonical {
		if event.Tool != nil && event.Tool.ID == "call-bad" {
			t.Fatalf("canonical events should not contain invalid tool call 'call-bad'")
		}
	}

	trace := evalharness.EventTrace(run.Events)
	uiOnlyInvalid := false
	for _, entry := range trace {
		if entry.Visibility == "ui_only" && entry.Tool != nil && entry.Tool.ID == "call-bad" {
			uiOnlyInvalid = true
		}
	}
	if !uiOnlyInvalid {
		t.Fatal("expected ui_only event for invalid tool call 'call-bad'")
	}
}
