package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

type codeModeAccountingModel struct {
	main, selector atomic.Int32
	yielded        bool
	release        chan struct{}
}

func (*codeModeAccountingModel) Name() string { return "accounting-fixture" }
func (*codeModeAccountingModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *codeModeAccountingModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		var message model.Message
		if len(req.Tools) == 1 && req.Tools[0].Function != nil && req.Tools[0].Function.Name == "InspectToolSchema" {
			if m.release != nil {
				select {
				case <-m.release:
				case <-ctx.Done():
					yield(nil, ctx.Err())
					return
				}
			}
			n := m.selector.Add(1)
			if n == 1 {
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect", Name: "InspectToolSchema", Args: `{"name":"docs__read"}`}}, "")
			} else {
				message = model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__read"]}`)
			}
		} else {
			n := m.main.Add(1)
			switch {
			case n == 1:
				args := `{"code":"return await tools.search('read documentation');"}`
				if m.yielded {
					args = `{"code":"return await tools.search('read documentation');","yield_time_ms":0}`
				}
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: args}}, "")
			case n == 2 && m.yielded:
				handle := jsExecTaskHandle(req)
				if handle == "" {
					yield(nil, errors.New("no original yielded Task handle"))
					return
				}
				close(m.release)
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "wait", Name: "Task", Args: `{"action":"wait","handle":"` + handle + `"}`}}, "")
			default:
				message = model.NewTextMessage(model.RoleAssistant, "done")
			}
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Usage: model.Usage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12, Reported: true}, TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted}), nil)
	}
}

func TestCodeModeAccountingSelectorReceiptsSurviveForeground(t *testing.T) {
	for _, scenario := range []string{"success", "later-request-rejected", "after-yield", "selector-request-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			llm := &codeModeAccountingModel{yielded: scenario == "after-yield"}
			if llm.yielded {
				llm.release = make(chan struct{})
			}
			def := fixtureMCPDefinition("v1")
			def.Name = "docs__read"
			def.Description = "read documentation"
			def.InputSchema = map[string]any{"type": "object"}
			source := &jsExecSource{items: []tool.Tool{tool.NamedTool{Def: def}}}
			tools := []tool.Tool{toolsearch.NewSource(source), tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
			var admissions atomic.Int32
			rejected := errors.New("reject later request")
			spec := agent.AgentSpec{Model: llm, Tools: tools, DeferredTools: source}
			spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: llm, Tools: tools, DeferredTools: source, Validate: func(context.Context) (func(), error) { return func() {}, nil }, Admit: func(_ context.Context, in agent.ModelRequestAdmission) error {
					if in.RequestID == "" || in.TurnID == "" {
						return errors.New("missing provider admission identity")
					}
					n := admissions.Add(1)
					if scenario == "later-request-rejected" && n == 4 || scenario == "selector-request-rejected" && n == 3 {
						return rejected
					}
					return nil
				}}, nil
			}
			rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "accounting fixture", AgentSpec: spec})
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := drainRunnerEvents(t, run.Handle)
			if scenario == "later-request-rejected" {
				if runErr == nil || !strings.Contains(runErr.Error(), rejected.Error()) {
					t.Fatalf("later rejection=%v", runErr)
				}
			} else if runErr != nil {
				t.Fatal(runErr)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if llm.yielded {
				waited := false
				for _, event := range events {
					if session.EventTypeOf(event) == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == tasktool.ToolName {
						if event.Tool.Status != "completed" || taskStringValue(event.Tool.Output["state"]) != string(taskapi.StateCompleted) {
							t.Fatalf("yielded script wait did not complete: status=%s output=%+v", event.Tool.Status, event.Tool.Output)
						}
						waited = true
					}
				}
				if !waited {
					t.Fatal("missing canonical wait result for the yielded script")
				}
				entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
				if err != nil || len(entries) != 1 {
					t.Fatalf("persisted script tasks=%d error=%v", len(entries), err)
				}
				entry := entries[0]
				raw := taskStringValue(entry.Result["tool_result_json"])
				var result tool.Result
				if err := json.Unmarshal([]byte(raw), &result); err != nil || result.IsError || !strings.Contains(raw, "docs__read") {
					t.Fatalf("persisted script result=%s error=%v", raw, err)
				}
				cursor, ok := taskInt64Value(entry.Metadata["model_output_cursor"])
				if entry.Kind != taskapi.KindCodeMode || entry.State != taskapi.StateCompleted || entry.Running || !ok || cursor != int64(len(raw)) {
					t.Fatalf("persisted script completion or selected output cursor missing: %+v", entry)
				}
			}
			receipts, tokens := 0, 0
			for _, event := range session.InvocationAccountingEvents(events) {
				if !session.IsModelInvocationReceipt(event) {
					continue
				}
				if event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] != "tool" {
					continue
				}
				receipts++
				tokens += session.UsageSnapshotFromSessionEvent(event).TotalTokens
				if session.IsMainInvocationVisibleEvent(event) || session.IsClientReplayEvent(event) {
					t.Fatal("private selector receipt entered model history")
				}
			}
			want := 2
			if scenario == "selector-request-rejected" {
				want = 1
			}
			if receipts != want || tokens != 12*want || int(llm.selector.Load()) != want {
				for _, event := range events {
					if event.Tool != nil {
						t.Logf("tool %s %s: %+v", event.Tool.Name, event.Tool.Status, event.Tool.Output)
					}
				}
				t.Fatalf("selector=%d receipts=%d tokens=%d want=%d/%d/%d", llm.selector.Load(), receipts, tokens, want, want, 12*want)
			}
			wantAdmissions := 4
			if llm.yielded {
				wantAdmissions = 5
			}
			if int(admissions.Load()) != wantAdmissions {
				t.Fatalf("admissions=%d want=%d", admissions.Load(), wantAdmissions)
			}
		})
	}
}

type codeModeAccountingFaultStore struct {
	session.Service
	failure   error
	committed bool
}

func (s codeModeAccountingFaultStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if session.IsModelInvocationReceipt(req.Event) && req.Event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "tool" {
		if s.committed {
			event, err := s.Service.AppendEvent(ctx, req)
			return event, errors.Join(err, s.failure)
		}
		return nil, s.failure
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestCodeModeAccountingReceiptWriteFailureAndUncertainAppend(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "committed-response-lost"}[committed], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			failure := errors.New("selector receipt persistence failed")
			wrapped := codeModeAccountingFaultStore{Service: store, failure: failure, committed: committed}
			llm := &codeModeAccountingModel{}
			def := fixtureMCPDefinition("v1")
			def.Name = "docs__read"
			def.InputSchema = map[string]any{"type": "object"}
			source := &jsExecSource{items: []tool.Tool{tool.NamedTool{Def: def}}}
			rt, err := New(Config{Sessions: wrapped, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "receipt fault", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainRunnerEvents(t, run.Handle)
			if committed && err != nil || !committed && !errors.Is(err, failure) {
				t.Fatalf("committed=%v result=%v", committed, err)
			}
			if llm.selector.Load() != 2 {
				t.Fatalf("selector replayed or skipped: %d", llm.selector.Load())
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, event := range session.InvocationAccountingEvents(events) {
				if session.IsModelInvocationReceipt(event) && event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "tool" {
					receipts++
				}
			}
			want := 0
			if committed {
				want = 2
			}
			if receipts != want {
				t.Fatalf("receipts=%d want=%d", receipts, want)
			}
		})
	}
}
