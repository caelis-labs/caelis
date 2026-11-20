package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	apptaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	"github.com/caelis-labs/caelis/control/taskstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == gojaexec.WorkerArgument {
		os.Exit(gojaexec.WorkerMain())
	}
	os.Exit(m.Run())
}

func TestRuntimeAgentCodeModeRunCommandOutput(t *testing.T) {
	const want = "acp-nested-command-output"
	command := "printf '%s' '" + want + "'"
	if runtime.GOOS == "windows" {
		command = "[Console]::Out.Write('" + want + "')"
	}
	args, err := json.Marshal(map[string]any{"command": command, "yield_time_ms": 5000})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		taskStream bool
	}{
		{name: "without_task_stream"},
		{name: "with_task_stream", taskStream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			llm := &codeModeCommandModel{script: "await RunCommand(" + string(args) + ");"}
			acpAgent := newCodeModeCommandAgent(t, cwd, llm, tc.taskStream)
			notifications := promptCodeModeCommand(t, cwd, acpAgent, newPromptRecorder(allowOncePermissionResponse()))
			childID := codeModeCommandCallID(t, notifications)
			if got := strings.Join(terminalOutputPayloads(notifications, childID), ""); got != want {
				t.Fatalf("nested RunCommand terminal output = %q, want exactly %q", got, want)
			}
		})
	}
}

func TestRuntimeAgentCodeModeRunCommandTaskStream(t *testing.T) {
	const want = "acp-nested-command-stream"
	cwd := t.TempDir()
	// No output is available in the yielded receipt: the process can print only
	// after the bridge attaches to the real Task subscription.
	command := "while [ ! -f release-command ]; do sleep 0.05; done; printf '%s' '" + want + "'"
	if runtime.GOOS == "windows" {
		command = "while (-not (Test-Path release-command)) { Start-Sleep -Milliseconds 50 }; [Console]::Out.Write('" + want + "')"
	}
	args, err := json.Marshal(map[string]any{"command": command, "yield_time_ms": 0})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &codeModeCommandStreamRecorder{promptRecorder: newPromptRecorder(allowOncePermissionResponse()), want: want, seen: make(chan struct{}, 1)}
	llm := &codeModeCommandModel{script: "await RunCommand(" + string(args) + ");", waitOutput: recorder.seen}
	acpAgent := newCodeModeCommandAgent(t, cwd, llm, true)
	notifications := promptCodeModeCommand(t, cwd, acpAgent, recorder)
	childID := codeModeCommandCallID(t, notifications)
	if got := strings.Join(terminalOutputPayloads(notifications, childID), ""); got != want {
		t.Fatalf("nested RunCommand TaskStream output = %q, want exactly %q", got, want)
	}
}

func newCodeModeCommandAgent(t *testing.T, cwd string, llm model.LLM, stream bool) *runtimeacp.RuntimeAgent {
	t.Helper()
	backend, err := host.New(host.Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	var recorder *taskstream.Recorder
	var spool *spoolfile.Store
	t.Cleanup(func() {
		_ = backend.Close()
		if recorder != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = recorder.Close(ctx)
		}
		if spool != nil {
			_ = spool.Close()
		}
	})
	command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(t.TempDir(), "sessions")})
	tasks := sessionfile.NewTaskStore(sessions)
	runtimeConfig := sdkruntime.Config{
		Sessions: sessions, TaskStore: tasks,
		Hooks: agent.Hooks{StreamMode: agent.StreamIncremental}, AgentFactory: chat.Factory{},
	}
	acpConfig := runtimeacp.Config{Sessions: sessions, AppName: "caelis", UserID: "user-1"}
	if stream {
		spool, err = spoolfile.New(t.Context(), spoolfile.Config{RootDir: filepath.Join(cwd, "spool")})
		if err != nil {
			t.Fatal(err)
		}
		recorder = taskstream.NewRecorder(spool, nil)
		runtimeConfig.TaskOutput = recorder
		service, err := taskstream.New(taskstream.Config{
			Tasks: tasks, Spool: spool, Recorder: recorder, Sessions: sessions,
			Authorizer: codeModeTaskAuthorizer{}, Secret: make([]byte, 32),
		})
		if err != nil {
			t.Fatal(err)
		}
		acpConfig.TaskStreams = &codeModeCommandStreamGate{Service: apptaskstream.New(service), path: filepath.Join(cwd, "release-command")}
		acpConfig.TaskStreamPrincipal = apptaskstream.Principal{ID: "user-1"}
	}
	rt, err := sdkruntime.New(runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	acpConfig.Runtime = rt
	acpConfig.BuildAgentSpec = func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
		return agent.AgentSpec{Name: "chat", Model: llm, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil
	}
	acpAgent, err := runtimeacp.New(acpConfig)
	if err != nil {
		t.Fatal(err)
	}
	return acpAgent
}

type codeModeCommandCallbacks interface {
	runtimeacp.PromptCallbacks
	Notifications() []eventstream.SessionNotification
}

func promptCodeModeCommand(t *testing.T, cwd string, acpAgent *runtimeacp.RuntimeAgent, recorder codeModeCommandCallbacks) []eventstream.SessionNotification {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	sess, err := acpAgent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	response, err := acpAgent.Prompt(ctx, runtimeacp.PromptInput{
		SessionID: string(sess.SessionId),
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run the script"}`)},
	}, recorder)
	if err != nil || response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("Prompt() = %+v, %v; want end turn", response, err)
	}
	return recorder.Notifications()
}

func codeModeCommandCallID(t *testing.T, notifications []eventstream.SessionNotification) string {
	t.Helper()
	var id string
	for _, notification := range notifications {
		if call, ok := notification.Update.(eventstream.ToolCall); ok && call.Name != nil && *call.Name == shell.RunCommandToolName {
			if id != "" && id != call.ToolCallID {
				t.Fatalf("multiple nested RunCommand calls: %q and %q", id, call.ToolCallID)
			}
			id = call.ToolCallID
		}
	}
	if id == "" {
		raw, _ := json.Marshal(notifications)
		t.Fatalf("no nested RunCommand tool_call: %s", raw)
	}
	return id
}

type codeModeCommandStreamGate struct {
	apptaskstream.Service
	path string
}

func (s *codeModeCommandStreamGate) Subscribe(ctx context.Context, p apptaskstream.Principal, req apptaskstream.SubscribeRequest) (apptaskstream.SubscribeResult, error) {
	result, err := s.Service.Subscribe(ctx, p, req)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(s.path, nil, 0600); err != nil {
		_ = result.Subscription.Close()
		return apptaskstream.SubscribeResult{}, err
	}
	return result, nil
}

type codeModeTaskAuthorizer struct{}

func (codeModeTaskAuthorizer) AuthorizeTaskStream(context.Context, taskstream.Principal, string) error {
	return nil
}

type codeModeCommandStreamRecorder struct {
	*promptRecorder
	want string
	seen chan struct{}
}

func (r *codeModeCommandStreamRecorder) SessionUpdate(ctx context.Context, n eventstream.SessionNotification) error {
	if err := r.promptRecorder.SessionUpdate(ctx, n); err != nil {
		return err
	}
	if update, ok := n.Update.(eventstream.ToolCallUpdate); ok && strings.Contains(strings.Join(terminalOutputPayloads(r.Notifications(), update.ToolCallID), ""), r.want) {
		select {
		case r.seen <- struct{}{}:
		default:
		}
	}
	return nil
}

type codeModeCommandModel struct {
	script     string
	called     bool
	waitOutput <-chan struct{}
}

func (*codeModeCommandModel) Name() string { return "code-mode-command-fixture" }

func (*codeModeCommandModel) Capabilities() model.Capabilities { return bridgeTestModelCapabilities() }

func (m *codeModeCommandModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "done")
		if !m.called {
			m.called = true
			args, _ := json.Marshal(map[string]any{"code": m.script, "yield_time_ms": 6000})
			message = model.NewMessage(model.RoleAssistant, model.NewToolUsePart("script-call", codemode.ToolName, args))
		} else if m.waitOutput != nil {
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-m.waitOutput:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-timer.C:
				yield(nil, fmt.Errorf("nested RunCommand output did not reach ACP via TaskStream"))
				return
			}
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Message: message, TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted,
		}}, nil)
	}
}
