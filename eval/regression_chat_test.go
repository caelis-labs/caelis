package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestRegressionChatMinimalToolLoopTrace(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("tool-loop",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-1",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "minimal-tool-loop",
		SessionID:    "sess-minimal-tool-loop",
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}
	if got, want := len(run.Requests), 2; got != want {
		t.Fatalf("len(Requests) = %d, want %d", got, want)
	}

	got := evalharness.StableJSON(evalharness.EventTrace(run.Events))
	want := `[
  {
    "visibility": "canonical",
    "type": "tool_call",
    "role": "assistant",
    "message_tool_calls": [
      {
        "id": "call-1",
        "name": "ECHO",
        "args": "{\"value\":\"pong\"}"
      }
    ],
    "tool": {
      "id": "call-1",
      "name": "ECHO",
      "status": "pending",
      "input": {
        "value": "pong"
      }
    }
  },
  {
    "visibility": "canonical",
    "type": "tool_result",
    "role": "tool",
    "tool": {
      "id": "call-1",
      "name": "ECHO",
      "status": "completed",
      "input": {
        "value": "pong"
      },
      "output": {
        "value": "pong"
      },
      "content": [
        {
          "type": "content",
          "text": "completed"
        }
      ]
    }
  },
  {
    "visibility": "canonical",
    "type": "assistant",
    "role": "assistant",
    "text": "pong"
  }
]`
	if got != want {
		t.Fatalf("minimal tool loop trace mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRegressionChatInvalidToolCallRetryTrace(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("invalid-then-valid-tool",
		evalharness.ToolCallStep("All checks pass. Now let me commit.", model.ToolCall{
			ID:   "call-invalid",
			Name: "ECHO",
			Args: `{"value":"pong"`,
		}),
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-valid",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "invalid-tool-retry",
		SessionID:    "sess-invalid-tool-retry",
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}
	if got, want := len(run.Requests), 3; got != want {
		t.Fatalf("len(Requests) = %d, want %d", got, want)
	}
	if got, want := evalharness.RequestMessagesJSON(run.Requests[1]), evalharness.RequestMessagesJSON(run.Requests[0]); got != want {
		t.Fatalf("retry request changed provider-visible messages\nfirst: %s\nretry: %s", want, got)
	}
	postToolMessages := evalharness.RequestMessagesJSON(run.Requests[2])
	if strings.Contains(postToolMessages, "All checks pass") || strings.Contains(postToolMessages, `{\"value\":\"pong\"`) {
		t.Fatalf("post-tool request retained invalid attempt state: %s", postToolMessages)
	}

	got := evalharness.StableJSON(evalharness.EventTrace(run.Events))
	want := `[
  {
    "visibility": "ui_only",
    "type": "lifecycle",
    "text": "model attempt reset"
  },
  {
    "visibility": "ui_only",
    "type": "assistant",
    "role": "assistant",
    "text": "All checks pass. Now let me commit."
  },
  {
    "visibility": "ui_only",
    "type": "tool_call",
    "tool": {
      "id": "call-invalid",
      "name": "ECHO",
      "status": "pending"
    }
  },
  {
    "visibility": "ui_only",
    "type": "tool_result",
    "tool": {
      "id": "call-invalid",
      "name": "ECHO",
      "status": "failed",
      "output": {
        "error": "decode tool call input for ECHO: unexpected EOF",
        "error_code": "invalid_input"
      },
      "content": [
        {
          "type": "content",
          "text": "decode tool call input for ECHO: unexpected EOF"
        }
      ]
    }
  },
  {
    "visibility": "canonical",
    "type": "tool_call",
    "role": "assistant",
    "message_tool_calls": [
      {
        "id": "call-valid",
        "name": "ECHO",
        "args": "{\"value\":\"pong\"}"
      }
    ],
    "tool": {
      "id": "call-valid",
      "name": "ECHO",
      "status": "pending",
      "input": {
        "value": "pong"
      }
    }
  },
  {
    "visibility": "canonical",
    "type": "tool_result",
    "role": "tool",
    "tool": {
      "id": "call-valid",
      "name": "ECHO",
      "status": "completed",
      "input": {
        "value": "pong"
      },
      "output": {
        "value": "pong"
      },
      "content": [
        {
          "type": "content",
          "text": "completed"
        }
      ]
    }
  },
  {
    "visibility": "canonical",
    "type": "assistant",
    "role": "assistant",
    "text": "pong"
  }
]`
	if got != want {
		t.Fatalf("invalid tool retry trace mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}
