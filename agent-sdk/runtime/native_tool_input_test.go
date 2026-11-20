package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
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

func TestRecoverableToolFailuresContinueBatchAndLoop(t *testing.T) {
	for _, mode := range []string{"serial", "parallel"} {
		t.Run(mode, func(t *testing.T) {
			store, active := newTestSessionService(t, "recoverable-batch")
			var dispatched sync.Map
			probe := tool.NamedTool{Def: tool.Definition{
				Name:        "probe",
				InputSchema: map[string]any{"type": "object", "required": []string{"path"}},
			}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				if _, already := dispatched.LoadOrStore(call.ID, true); already {
					t.Errorf("call %s executed twice", call.ID)
				}
				switch call.ID {
				case "unavailable":
					return tool.Result{}, errorcode.New(errorcode.Unavailable, "backend temporarily unavailable")
				case "timeout":
					return tool.Result{}, context.DeadlineExceeded
				case "ordinary":
					return tool.Result{}, errors.New("file was removed")
				case "domain":
					return tool.Result{Content: []tool.ContentBlock{tool.Text("document is locked")}}, tool.FailedReply("fixture tool failure")
				case "sibling", "corrected":
					return tool.Result{Content: []tool.ContentBlock{tool.Text("ok")}}, nil
				default:
					t.Errorf("unexpected dispatch: %s", call.ID)
					return tool.Result{}, errors.New("unexpected call")
				}
			}}
			var batch []model.ToolCall
			for _, id := range []string{"schema", "unavailable", "timeout", "ordinary", "domain", "sibling"} {
				input := `{"path":"sample"}`
				if id == "schema" {
					input = "{}"
				}
				batch = append(batch, model.ToolCall{ID: id, Name: "probe", Args: input})
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return batch
				case 1:
					return []model.ToolCall{{ID: "corrected", Name: "probe", Args: `{"path":"other"}`}}
				default:
					return nil
				}
			}}
			hooks := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "observe", Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
				return next(ctx)
			}}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			var llm model.LLM = driver
			if mode == "serial" {
				llm = serialToolCallModel{driver}
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "process documents", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{probe}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(driver.requests) != 3 {
				t.Fatalf("model received %d requests, want 3", len(driver.requests))
			}
			for _, call := range batch {
				result := errorDeliveryModelResult(t, driver.requests[1], call.ID)
				if result.IsError != (call.ID != "sibling") {
					t.Errorf("wrong result for %s: %+v", call.ID, result)
				}
				if _, ran := dispatched.Load(call.ID); ran != (call.ID != "schema") {
					t.Errorf("call %s dispatched=%t", call.ID, ran)
				}
			}
			if result := errorDeliveryModelResult(t, driver.requests[2], "corrected"); result.IsError {
				t.Fatalf("correction failed: %+v", result)
			}
		})
	}
}

type serialToolCallModel struct{ *mcpScriptModel }

func (serialToolCallModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}
