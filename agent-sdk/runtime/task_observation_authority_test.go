package runtime

import (
	"context"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

// Keep both native observations in one model batch while controlling whether
// read captures running state or the deferred terminal entry after wait returns.
type commandObservationOrder struct {
	command      *concurrentCommandWaitSession
	afterWait    bool
	waitReturned chan struct{}
}

func (o commandObservationOrder) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	if event.Operation != agent.LifecycleTool {
		return next(ctx)
	}
	if event.StepID == "terminal-wait" {
		err := next(ctx)
		close(o.waitReturned)
		return err
	}
	if event.StepID != "snapshot-read" {
		return next(ctx)
	}
	select {
	case <-o.command.waitStarted:
	case <-ctx.Done():
		return ctx.Err()
	}
	if o.afterWait {
		o.command.complete()
		select {
		case <-o.waitReturned:
		case <-ctx.Done():
			return ctx.Err()
		}
		return next(ctx)
	}
	err := next(ctx)
	o.command.complete()
	return err
}

func TestRuntimeConcurrentTaskObservationsPreserveProducerCompletion(t *testing.T) {
	t.Parallel()
	for _, afterWait := range []bool{false, true} {
		name := "earlier-running-read"
		if afterWait {
			name = "later-terminal-read"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			root := t.TempDir()
			files := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := files.StartSession(ctx, session.StartSessionRequest{
				AppName: "test", UserID: "owner", Workspace: session.WorkspaceRef{CWD: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			command := newConcurrentCommandWaitSession()
			command.stdout = "producer final result\n"
			command.result.Stdout = command.stdout
			command.result.ExitCode = 0
			rt, err := New(Config{
				Sessions: files, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{},
				DefaultPolicyMode:     presets.ModeAutoReview,
				LifecycleInterceptors: []agent.LifecycleInterceptor{commandObservationOrder{command: command, afterWait: afterWait, waitReturned: make(chan struct{})}},
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := rt.tasks.StartCommand(ctx, active, active.SessionRef, newCommandStartProbe(command, nil), taskapi.CommandStartRequest{
				Command: "controlled producer", Workdir: active.CWD, Yield: 0,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !started.Running {
				t.Fatalf("initial Task = %+v, want a running producer", started)
			}
			step := 0
			var liveContext []model.Message
			driver := &commandApprovalModel{generate: func(_ context.Context, req *model.Request) model.Message {
				step++
				if step == 1 {
					return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
						{ID: "terminal-wait", Name: tasktool.ToolName, Args: string(mustJSONRaw(map[string]any{"action": "wait", "handle": started.Handle}))},
						{ID: "snapshot-read", Name: tasktool.ToolName, Args: string(mustJSONRaw(map[string]any{"action": "read", "handle": started.Handle}))},
					}, "")
				}
				liveContext = model.CloneMessages(req.Messages)
				return model.NewTextMessage(model.RoleAssistant, "done")
			}}
			run, err := rt.Run(ctx, agent.RunRequest{
				SessionRef: active.SessionRef, Input: "wait and inspect the same command concurrently",
				AgentSpec: agent.AgentSpec{Name: "chat", Model: driver, Tools: []tool.Tool{tasktool.New()}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Handle.Cancel()
			if err := run.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			events, err := files.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			var observed []string
			for _, event := range events {
				if session.EventTypeOf(event) == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == tasktool.ToolName {
					observed = append(observed, taskStringValue(event.Tool.Output["state"]))
				}
			}
			wantStates := []string{string(taskapi.StateCompleted), string(taskapi.StateRunning)}
			if afterWait {
				wantStates[1] = string(taskapi.StateCompleted)
			}
			if !reflect.DeepEqual(observed, wantStates) {
				t.Fatalf("canonical observations = %v, want %v", observed, wantStates)
			}
			assertCompleted := func(store taskapi.Store) {
				t.Helper()
				entry, err := store.Get(ctx, started.Ref.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if entry.State != taskapi.StateCompleted || entry.Running || taskRawStringValue(entry.Result["result"]) != command.stdout {
					t.Fatalf("producer completion overwritten by old observation: state=%s running=%v result=%#v", entry.State, entry.Running, entry.Result)
				}
				if code, ok := taskInt64Value(entry.Result["exit_code"]); !ok || code != 0 {
					t.Fatalf("producer exit code = %#v, want zero", entry.Result["exit_code"])
				}
				if cursor, ok := taskInt64Value(entry.Metadata["output_cursor"]); !ok || cursor != int64(len(command.stdout)) {
					t.Fatalf("canonical output cursor = %#v, want retained final-text length", entry.Metadata["output_cursor"])
				}
			}
			assertCompleted(rt.tasks.store)
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			assertCompleted(sessionfile.NewTaskStore(reopened))
			probe := &recoveryCaptureModel{}
			restored, err := New(Config{Sessions: reopened, TaskStore: sessionfile.NewTaskStore(reopened), AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := restored.Run(ctx, agent.RunRequest{
				SessionRef: active.SessionRef, Input: "continue after reload", AgentSpec: agent.AgentSpec{Name: "chat", Model: probe},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := resumed.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			wantContext := append(liveContext, model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "continue after reload"))
			if !reflect.DeepEqual(probe.messages, wantContext) {
				t.Fatalf("reloaded model context differs from live context: got=%#v want=%#v", probe.messages, wantContext)
			}
		})
	}
}
