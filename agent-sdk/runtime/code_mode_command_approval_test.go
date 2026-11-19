package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeCommandApprovalUsesOriginalOwner(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			commandText := "printf once > effect.txt"
			if goruntime.GOOS == "windows" {
				commandText = "Set-Content -Path effect.txt -Value once -NoNewline"
			}
			scriptArgs, _ := json.Marshal(map[string]any{"code": "return await RunCommand({command:" + stringJSONForCommandTest(commandText) + "});", "yield_time_ms": 0})
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			asked, release := make(chan string, 1), make(chan struct{})
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == "RunCommand" {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if req.Tool.Name != "RunCommand" || req.Call.Parent == nil || req.Call.Parent.ID != "script" {
					t.Error("approval lost real command target/direct parent")
				}
				asked <- req.OperationTaskID
				select {
				case <-release:
				case <-ctx.Done():
				}
				option := "allow_once"
				if mode == "deny" {
					option = "cancel"
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: mode != "deny"}, nil
			})
			var commandHandle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(scriptArgs)}}
				}
				if i == 1 {
					select {
					case commandID := <-asked:
						if mode == "cancel" {
							entries, err := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
							if err != nil {
								t.Fatal(err)
							}
							for _, entry := range entries {
								if entry.TaskID == commandID {
									commandHandle = entry.Handle
								}
							}
							if commandHandle == "" {
								t.Fatal("pending approval lost its original command Task")
							}
						}
					case <-time.After(5 * time.Second):
						t.Error("command never reached original approval")
						return nil
					}
					if _, err := os.Stat(filepath.Join(root, "effect.txt")); !os.IsNotExist(err) {
						t.Error("command executed before approval")
					}
					action := "wait"
					if mode == "cancel" {
						action = "cancel"
					} else {
						close(release)
					}
					handle := jsExecTaskHandle(r)
					if mode == "cancel" {
						handle = commandHandle
					}
					raw, _ := json.Marshal(map[string]any{"action": action, "handle": handle})
					return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}, registry, requester)
			raw, err := os.ReadFile(filepath.Join(root, "effect.txt"))
			if mode == "allow" {
				if err != nil || string(raw) != "once" {
					t.Fatalf("actual command effect=%q %v", raw, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("rejected/cancelled command effect=%q %v", raw, err)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Running {
					t.Fatalf("producer did not close: %+v", entry)
				}
			}
		})
	}
}

func stringJSONForCommandTest(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestCodeModeCommandApprovalAfterScriptExit(t *testing.T) {
	startErr := errors.New("test sandbox could not start command")
	persistErr := errors.New("test command terminal write failed")
	for _, tc := range []struct {
		name       string
		wantStarts int32
		wantState  taskapi.State
		wantError  string
		exitCode   int
		startErr   error
	}{
		{name: "allow", wantStarts: 1, wantState: taskapi.StateCompleted},
		{name: "reset", wantStarts: 1, wantState: taskapi.StateCompleted},
		{name: "nonzero", wantStarts: 1, wantState: taskapi.StateFailed, exitCode: 7},
		{name: "start-failure", wantStarts: 1, wantState: taskapi.StateFailed, startErr: startErr},
		{name: "stale", wantState: taskapi.StateFailed, wantError: "stale_revision"},
		{name: "cancel", wantState: taskapi.StateCancelled},
		{name: "turn-complete", wantState: taskapi.StateCancelled},
		{name: "settlement-failure", wantStarts: 1, startErr: startErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			native, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			backend := &codeModeCommandSandbox{Runtime: native, startErr: tc.startErr}
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == shell.RunCommandToolName {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			approve := make(chan struct{})
			asked := make(chan agent.ApprovalRequest, 1)
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				asked <- req
				if err := req.OnAdmission(ctx); err != nil {
					return agent.ApprovalResponse{}, err
				}
				select {
				case <-approve:
					return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
				case <-ctx.Done():
					return agent.ApprovalResponse{}, ctx.Err()
				}
			})
			var stale atomic.Bool
			var pending struct {
				Handle string
				State  taskapi.State
			}
			target := filepath.Join(root, "command-effect.txt")
			commandText := shellWriteFileForTest(target, "once")
			if tc.exitCode != 0 {
				commandText = "exit 7"
			}
			script := "let retainedCommandState = 41; return (await RunCommand({command:" + stringJSONForCommandTest(commandText) + ",yield_time_ms:0})).json();"
			driver := &mcpScriptModel{steps: func(step int, req *model.Request) []model.ToolCall {
				switch step {
				case 0:
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(mustJSONRaw(map[string]any{"code": script}))}}
				case 1:
					result := errorDeliveryModelResult(t, req, "script")
					var raw []byte
					if len(result.Content) == 1 {
						raw = result.Content[0].JSONValue()
					}
					if result.IsError || json.Unmarshal(raw, &pending) != nil ||
						pending.State != taskapi.StateWaitingApproval || pending.Handle == "" || backend.starts.Load() != 0 {
						t.Errorf("script did not finish with a pending command and no effect: %s", raw)
						close(approve)
						return nil
					}
					if tc.name == "turn-complete" {
						return []model.ToolCall{{ID: "complete", Name: "Complete", Args: "{}"}}
					}
					args := `{"code":"return retainedCommandState;"}`
					if tc.name == "reset" {
						args = `{"reset":true}`
					}
					return []model.ToolCall{{ID: "next-script", Name: codemode.ToolName, Args: args}}
				case 2:
					result := errorDeliveryModelResult(t, req, "next-script")
					if result.IsError || len(result.Content) != 1 || (tc.name != "reset" && string(result.Content[0].JSONValue()) != "41") {
						t.Errorf("next script did not reuse the idle environment: %+v", result)
					}
					stale.Store(tc.name == "stale")
					action := "wait"
					if tc.name == "cancel" {
						action = "cancel"
					} else {
						close(approve)
					}
					if tc.name != "settlement-failure" {
						return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(mustJSONRaw(map[string]any{"action": action, "handle": pending.Handle}))}}
					}
				case 3:
					result := errorDeliveryModelResult(t, req, "observe")
					var payload struct {
						State    taskapi.State
						Handle   string
						Code     string `json:"error_code"`
						Error    string
						ExitCode int `json:"exit_code"`
					}
					if result.IsError || len(result.Content) != 1 || json.Unmarshal(result.Content[0].JSONValue(), &payload) != nil ||
						payload.Handle != pending.Handle || payload.State != tc.wantState ||
						payload.Code != tc.wantError || payload.ExitCode != tc.exitCode ||
						(tc.startErr != nil && payload.Error != tc.startErr.Error()) {
						t.Errorf("model did not receive the original command outcome: %+v", result)
					}
				}
				return nil
			}}
			complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}},
				Invoke: func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{TurnComplete: true}, nil }}
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: driver, Tools: []tool.Tool{command, tasktool.New(), complete,
					&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: native}},
					Validate: func(context.Context) (func(), error) {
						if stale.Load() {
							return nil, agent.ErrModelRequestSnapshotStale
						}
						return func() {}, nil
					}}, nil
			}}
			tasks := sessionfile.NewTaskStore(store)
			var taskStore taskapi.Store = tasks
			if tc.name == "settlement-failure" {
				taskStore = &codeModeCommandFailingStore{TaskStore: tasks, err: persistErr}
			}
			rt, err := New(Config{Sessions: store, TaskStore: taskStore, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rt.ReleaseCodeModeEnvironments() }()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "submit a command", AgentSpec: spec, ApprovalRequester: requester})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Handle.Close()
			err = run.Handle.WaitCompletion(ctx)
			if tc.name == "settlement-failure" {
				if !errors.Is(err, persistErr) {
					t.Fatalf("Run completion = %v, want terminal persistence failure", err)
				}
			} else if err != nil {
				t.Fatalf("Run completion = %v, want success", err)
			}
			if starts := backend.starts.Load(); starts != tc.wantStarts {
				t.Errorf("command starts = %d, want %d", starts, tc.wantStarts)
			}
			var request agent.ApprovalRequest
			select {
			case request = <-asked:
			default:
				t.Fatal("command did not reach approval")
			}
			entry, err := tasks.Get(ctx, request.OperationTaskID)
			if err != nil || entry == nil || entry.Handle != pending.Handle || (tc.wantState != "" && entry.State != tc.wantState) {
				t.Fatalf("original durable command = %+v, %v", entry, err)
			}
			data, err := os.ReadFile(target)
			if tc.wantState == taskapi.StateCompleted && (err != nil || string(data) != "once") {
				t.Fatalf("command effect = %q, %v", data, err)
			}
			if tc.wantState != taskapi.StateCompleted && !os.IsNotExist(err) {
				t.Fatalf("unexpected command effect = %q, %v", data, err)
			}
			if tc.name == "start-failure" {
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
				resumed, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				probe := &recoveryCaptureModel{}
				next, err := resumed.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{Model: probe}})
				if err != nil {
					t.Fatal(err)
				}
				defer next.Handle.Close()
				if err := next.Handle.WaitCompletion(ctx); err != nil {
					t.Fatal(err)
				}
				live := errorDeliveryModelResult(t, driver.requests[3], "observe")
				replayed := errorDeliveryModelResult(t, &model.Request{Messages: probe.messages}, "observe")
				if !reflect.DeepEqual(live, replayed) {
					t.Fatalf("replayed command failure = %+v, want live model result %+v", replayed, live)
				}
			}
		})
	}
}

type codeModeCommandSandbox struct {
	sandbox.Runtime
	starts   atomic.Int32
	startErr error
}

func (s *codeModeCommandSandbox) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	s.starts.Add(1)
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.Runtime.Start(ctx, req)
}

type codeModeCommandFailingStore struct {
	*sessionfile.TaskStore
	err error
}

func (s *codeModeCommandFailingStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry.Kind == taskapi.KindCommand && req.Entry.State == taskapi.StateFailed {
		return nil, s.err
	}
	return s.TaskStore.Put(ctx, req)
}
