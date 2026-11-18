package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

func TestModelCanCorrectInvalidReadArguments(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks agent.Hooks
	}{
		{name: "without-hook"},
		{name: "observing-hook", hooks: agent.Hooks{ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "observe", Apply: func(_ context.Context, _ agent.HookContext, p agent.ToolPreparation) (agent.ToolPreparation, error) {
			return p, nil
		}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active := newTestSessionService(t, "invalid-read")
			file := filepath.Join(active.CWD, "sample.txt")
			if err := os.WriteFile(file, []byte("done"), 0o600); err != nil {
				t.Fatal(err)
			}
			backend := hostRuntimeForTest(t, active.CWD)
			read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), backend)
			if err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(map[string]any{"path": file})
			if err != nil {
				t.Fatal(err)
			}
			m := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "invalid", Name: "Read", Args: "{}"}}
				case 1:
					results := r.Messages[len(r.Messages)-1].ToolResults()
					if len(results) != 1 || results[0].ToolUseID != "invalid" || !results[0].IsError {
						t.Error("expected error for invalid read")
					}
					return []model.ToolCall{{ID: "corrected", Name: "Read", Args: string(input)}}
				case 2:
					results := r.Messages[len(r.Messages)-1].ToolResults()
					if len(results) != 1 || results[0].ToolUseID != "corrected" || results[0].IsError {
						t.Error("corrected read failed")
					}
				}
				return nil
			}}
			rt, err := New(Config{Sessions: store, Hooks: tc.hooks, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read", AgentSpec: agent.AgentSpec{Model: m, Tools: []tool.Tool{read}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatalf("completed model requests=%d, run error=%v", len(m.requests), err)
			}
			if len(m.requests) != 3 {
				t.Fatalf("requests=%d", len(m.requests))
			}
		})
	}
}
