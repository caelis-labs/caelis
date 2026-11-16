package gatewayapp_test

import (
	"context"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	appstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	"github.com/caelis-labs/caelis/control/taskstream"
)

type scriptStreamModel struct {
	calls   atomic.Int32
	entered <-chan struct{}
	settled func(context.Context) error
}

func (*scriptStreamModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (*scriptStreamModel) Name() string { return "local-task-fixture" }
func (m *scriptStreamModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		n := m.calls.Add(1)
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true}
		if n == 1 {
			response = &model.Response{Message: model.NewMessage(model.RoleAssistant, model.NewToolUsePart("script", "js_exec", []byte(`{"code":"await Hold({});return {answer:42};","yield_time_ms":0}`)))}
		}
		if n == 2 {
			// Do not end the model Turn before the yielded worker has entered
			// its controlled Host wait. This is a late-success observation test,
			// not a race against the original TurnComplete admission boundary.
			select {
			case <-m.entered:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
			response = &model.Response{Message: model.NewMessage(model.RoleAssistant, model.NewToolUsePart("release", "Release", []byte(`{}`)))}
		}
		if n == 3 {
			// Keep the original Run open until its producer has persisted the
			// terminal result. TurnComplete may legitimately cancel unfinished
			// work, even after Hold has returned to the bridge.
			if err := m.settled(ctx); err != nil {
				yield(nil, err)
				return
			}
		}
		response.TurnComplete = true
		response.StepComplete = true
		response.Status = model.ResponseStatusCompleted
		response.FinishReason = model.FinishReasonStop
		if n < 3 {
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

type scriptStreamAuth struct{}

func (scriptStreamAuth) AuthorizeTaskStream(context.Context, taskstream.Principal, string) error {
	return nil
}

func TestScriptRuntimeTaskTerminalWithoutTaskRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: root, CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := sessionfile.NewTaskStore(store)
	spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	recorder := taskstream.NewRecorder(spool, nil)
	defer recorder.Close(ctx)
	policies, err := policy.NewMemory(policy.NamedMode{ID: "allow", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Config{Sessions: store, TaskStore: tasks, TaskOutput: recorder, AgentFactory: chat.Factory{}, PolicyRegistry: policies, DefaultPolicyMode: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(entered)
		select {
		case <-release:
			return tool.Result{}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	next := tool.NamedTool{Def: tool.Definition{Name: "Release", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { close(release); return tool.Result{}, nil }}
	settled := func(ctx context.Context) error {
		for {
			entries, err := tasks.ListSession(ctx, active.SessionRef)
			if err != nil {
				return err
			}
			if len(entries) == 1 && !entries[0].Running {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "fixture", AgentSpec: agent.AgentSpec{Model: &scriptStreamModel{entered: entered, settled: settled}, Tools: []tool.Tool{hold, next, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := tasks.ListSession(ctx, active.SessionRef)
	if err != nil || len(entries) != 1 || entries[0].Running {
		t.Fatalf("unsettled original Task: %+v %v", entries, err)
	}
	service, err := taskstream.New(taskstream.Config{Tasks: tasks, Spool: spool, Recorder: recorder, Sessions: store, Authorizer: scriptStreamAuth{}, Secret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	// Late observation reads the real producer spool. It is not a Task.read call
	// and must preserve both parent completion and the selected business output.
	response, err := appstream.New(service).Events(ctx, taskstream.Principal{}, taskstream.ReadRequest{SessionID: active.SessionID, TaskID: entries[0].TaskID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, delivery := range response.Deliveries {
		for _, env := range delivery.Events {
			if update, ok := env.Update.(eventstream.ToolCallUpdate); ok && update.ToolCallID == "script" && update.Status != nil && *update.Status == "completed" {
				found = true
				if update.Meta["code_mode_script_completion"] != "completed" || len(update.Content) == 0 {
					t.Fatalf("terminal producer frame lost selected result: %+v", update)
				}
			}
		}
	}
	if !found {
		t.Fatalf("late Control observer never received original script terminal without Task.read: tasks=%+v response=%+v", entries, response)
	}
}
