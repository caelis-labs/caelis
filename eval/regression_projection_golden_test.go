package eval

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/evalharness"
	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type projectionTraceEntry struct {
	Kind             string `json:"kind,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	NarrativeRole    string `json:"narrative_role,omitempty"`
	NarrativeText    string `json:"narrative_text,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	ToolCallName     string `json:"tool_call_name,omitempty"`
	ToolCallStatus   string `json:"tool_call_status,omitempty"`
	ToolResultID     string `json:"tool_result_id,omitempty"`
	ToolResultName   string `json:"tool_result_name,omitempty"`
	ToolResultStatus string `json:"tool_result_status,omitempty"`
}

func projectionTrace(envs []gateway.EventEnvelope) []projectionTraceEntry {
	out := make([]projectionTraceEntry, 0, len(envs))
	for _, env := range envs {
		entry := projectionTraceEntry{
			Kind:   string(env.Event.Kind),
			Cursor: env.Cursor,
		}
		if env.Event.Narrative != nil {
			entry.NarrativeRole = string(env.Event.Narrative.Role)
			entry.NarrativeText = env.Event.Narrative.Text
		}
		if env.Event.ToolCall != nil {
			entry.ToolCallID = env.Event.ToolCall.CallID
			entry.ToolCallName = env.Event.ToolCall.ToolName
			entry.ToolCallStatus = string(env.Event.ToolCall.Status)
		}
		if env.Event.ToolResult != nil {
			entry.ToolResultID = env.Event.ToolResult.CallID
			entry.ToolResultName = env.Event.ToolResult.ToolName
			entry.ToolResultStatus = string(env.Event.ToolResult.Status)
		}
		out = append(out, entry)
	}
	return out
}

func TestRegressionProjectionGoldenToolLoop(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-tool-loop",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-1",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "proj-tool-loop",
		SessionID:    "sess-proj-tool-loop",
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	ref := session.SessionRef{SessionID: "sess-proj-tool-loop"}
	var envs []gateway.EventEnvelope
	for _, event := range run.Events {
		if env, ok := kernelimpl.ProjectSessionEvent(ref, event); ok {
			envs = append(envs, env)
		}
	}

	got := evalharness.StableJSON(projectionTrace(envs))
	want := `[
  {
    "kind": "tool_call",
    "tool_call_id": "call-1",
    "tool_call_name": "ECHO",
    "tool_call_status": "started"
  },
  {
    "kind": "tool_result",
    "tool_result_id": "call-1",
    "tool_result_name": "ECHO",
    "tool_result_status": "completed"
  },
  {
    "kind": "assistant_message",
    "narrative_role": "assistant",
    "narrative_text": "pong"
  }
]`
	if got != want {
		t.Fatalf("projection golden mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRegressionProjectionGoldenReasoning(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-reasoning",
		evalharness.AssistantPartsStep("the answer", "internal chain of thought"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "proj-reasoning",
		SessionID:    "sess-proj-reasoning",
		Prompt:       "what is 2+2",
		SystemPrompt: "Think step by step.",
		Model:        scripted,
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	ref := session.SessionRef{SessionID: "sess-proj-reasoning"}
	var envs []gateway.EventEnvelope
	for _, event := range run.Events {
		if env, ok := kernelimpl.ProjectSessionEvent(ref, event); ok {
			envs = append(envs, env)
		}
	}

	trace := projectionTrace(envs)
	if len(trace) != 1 {
		t.Fatalf("expected 1 projected event, got %d", len(trace))
	}

	entry := trace[0]
	if entry.Kind != "assistant_message" {
		t.Fatalf("kind = %q, want assistant_message", entry.Kind)
	}
	if entry.NarrativeRole != "assistant" {
		t.Fatalf("narrative_role = %q, want assistant", entry.NarrativeRole)
	}
	if entry.NarrativeText != "the answer" {
		t.Fatalf("narrative_text = %q, want 'the answer'", entry.NarrativeText)
	}

	env := envs[0]
	if env.Event.Narrative == nil {
		t.Fatal("expected narrative payload")
	}
	if env.Event.Narrative.ReasoningText != "internal chain of thought" {
		t.Fatalf("reasoning_text = %q, want 'internal chain of thought'", env.Event.Narrative.ReasoningText)
	}
}

func TestRegressionProjectionGoldenMultiTool(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-multi-tool",
		evalharness.ToolCallStep("looking...", model.ToolCall{
			ID:   "call-a",
			Name: "READ",
			Args: `{"path":"main.go"}`,
		}, model.ToolCall{
			ID:   "call-b",
			Name: "SEARCH",
			Args: `{"path":".","pattern":"func main"}`,
		}),
		evalharness.TextStep("done"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "proj-multi-tool",
		SessionID:    "sess-proj-multi-tool",
		Prompt:       "inspect",
		SystemPrompt: "",
		Model:        scripted,
		Tools: []tool.Tool{
			evalharness.EchoTool("READ"),
			evalharness.EchoTool("SEARCH"),
		},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	ref := session.SessionRef{SessionID: "sess-proj-multi-tool"}
	var envs []gateway.EventEnvelope
	for _, event := range run.Events {
		if env, ok := kernelimpl.ProjectSessionEvent(ref, event); ok {
			envs = append(envs, env)
		}
	}

	trace := projectionTrace(envs)

	toolCallCount := 0
	toolResultCount := 0
	for _, entry := range trace {
		switch entry.Kind {
		case "tool_call":
			toolCallCount++
		case "tool_result":
			toolResultCount++
		}
	}
	if toolCallCount != 2 {
		t.Fatalf("expected 2 tool_call projections, got %d", toolCallCount)
	}
	if toolResultCount != 2 {
		t.Fatalf("expected 2 tool_result projections, got %d", toolResultCount)
	}

	for _, entry := range trace {
		if entry.Kind == "tool_call" && entry.ToolCallID == "call-a" {
			if entry.ToolCallStatus != "started" {
				t.Fatalf("tool call a status = %q, want started", entry.ToolCallStatus)
			}
		}
		if entry.Kind == "tool_result" && entry.ToolResultID == "call-a" {
			if entry.ToolResultStatus != "completed" {
				t.Fatalf("tool result a status = %q, want completed", entry.ToolResultStatus)
			}
		}
	}
}
