package subagent

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

// This runs the model-facing tools through Runtime and a real standard ACP
// subprocess. Only the explicitly read final result enters parent context.
func TestRuntimeACPSpawnMessageWaitContextRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "active")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := runner.Quiesce(cleanup); err != nil {
			t.Errorf("Quiesce: %v", err)
		}
	})
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	tasks := sessionfile.NewTaskStore(store)
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "collaboration", UserID: "isolated-test", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	core, err := runtime.New(runtime.Config{Sessions: store, TaskStore: tasks, AgentFactory: chat.Factory{}, Subagents: runner})
	if err != nil {
		t.Fatal(err)
	}
	probe := &collaborationModel{}
	run, err := core.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "collaborate", AgentSpec: agent.AgentSpec{
		Name: "parent", Model: probe, Tools: []tool.Tool{spawn.New([]delegation.Agent{{Name: "helper"}}), sendmessage.New(), tasktool.New()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	if len(probe.requests) != 4 {
		t.Fatalf("model calls = %d, want 4", len(probe.requests))
	}
	beforeWait, _ := json.Marshal(probe.requests[2])
	if strings.Contains(string(beforeWait), "steered output") {
		t.Fatalf("child output leaked before Task read: %s", beforeWait)
	}
	var ids []string
	for _, message := range probe.requests[3] {
		for _, result := range message.ToolResults() {
			if result.IsError {
				t.Fatalf("tool %s failed: %#v", result.ToolUseID, result)
			}
			ids = append(ids, result.ToolUseID)
			if result.ToolUseID == "wait-child" {
				encoded, _ := json.Marshal(result)
				if !strings.Contains(string(encoded), "steered output") {
					t.Fatalf("Task final missing: %s", encoded)
				}
			}
		}
	}
	if !reflect.DeepEqual(ids, []string{"spawn-child", "message-child", "wait-child"}) {
		t.Fatalf("result call ownership/order = %v", ids)
	}
	entries, err := tasks.ListSession(ctx, active.SessionRef)
	if err != nil || len(entries) != 1 {
		t.Fatalf("tasks = %#v, %v", entries, err)
	}
	if entries[0].State != task.StateCompleted || entries[0].Running || entries[0].Session.SessionID != active.SessionID {
		t.Fatalf("task identity/terminal = %#v", entries[0])
	}
	// Reopen actual disk state and let Runtime build the next model request.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	restored, err := runtime.New(runtime.Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	replay := &collaborationModel{passive: true}
	next, err := restored.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "resume", AgentSpec: agent.AgentSpec{Name: "parent", Model: replay}})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	want := append(model.CloneMessages(probe.requests[3]), model.NewTextMessage(model.RoleAssistant, "parent done"), model.NewTextMessage(model.RoleUser, "resume"))
	if len(replay.requests) != 1 || !reflect.DeepEqual(replay.requests[0], want) {
		t.Fatalf("reopened model context = %#v, want %#v", replay.requests, want)
	}
}

type collaborationModel struct {
	passive  bool
	requests [][]model.Message
}

func (*collaborationModel) Name() string { return "isolated-collaboration" }
func (*collaborationModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *collaborationModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneMessages(request.Messages))
	step := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop, Message: model.NewTextMessage(model.RoleAssistant, "parent done")}
		calls := []model.ToolCall{
			{ID: "spawn-child", Name: "Spawn", Args: `{"agent":"helper","handle":"worker","prompt":"initial"}`},
			{ID: "message-child", Name: "SendMessage", Args: `{"to":"worker","message":"guide now"}`},
			{ID: "wait-child", Name: "Task", Args: `{"action":"wait","handle":"worker"}`},
		}
		if !m.passive && step <= len(calls) {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{calls[step-1]}, "")
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}
