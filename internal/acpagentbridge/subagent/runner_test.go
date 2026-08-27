package subagent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type detachedChildContextMarker struct{}

func TestSubagentSessionMetaMarksParentAndTask(t *testing.T) {
	t.Parallel()

	meta := subagentSessionMeta(tasksubagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-session"},
		TaskID:     "task-1",
	})
	path := []string{metautil.Root, metautil.Runtime, metautil.RuntimeSession}
	if got := metautil.String(meta, append(path, metautil.RuntimeSessionKind)...); got != metautil.RuntimeSessionKindSubagent {
		t.Fatalf("session kind = %q, want subagent", got)
	}
	if got := metautil.String(meta, append(path, metautil.RuntimeSessionParentID)...); got != "parent-session" {
		t.Fatalf("parent Session ID = %q, want parent-session", got)
	}
	if got := metautil.String(meta, append(path, metautil.RuntimeTaskID)...); got != "task-1" {
		t.Fatalf("Task ID = %q, want task-1", got)
	}
	if got := metautil.String(meta, append(path, metautil.RuntimeSessionHistoryToken)...); got != "" {
		t.Fatalf("ordinary Session metadata history token = %q, want empty", got)
	}

	historyMeta := subagentHistorySessionMeta(tasksubagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-session"},
		TaskID:     "task-1",
	}, strings.Repeat("ab", 32))
	if got := metautil.String(historyMeta, append(path, metautil.RuntimeSessionHistoryToken)...); got != strings.Repeat("ab", 32) {
		t.Fatalf("history Session metadata token = %q, want process capability", got)
	}
}

func TestChildACPUpdatePreservesUIOnlyRuntimeToolIdentity(t *testing.T) {
	t.Parallel()

	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SendMessage",
	})
	envelope := client.UpdateEnvelope{
		SessionID: "child-session",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "message-1",
			Title:         "Send message to @tova: ready",
			Kind:          "other",
			Meta:          meta,
		},
	}
	run := &childRun{
		anchor: delegation.Anchor{TaskID: "task-1", SessionID: "child-session", AgentID: "child-1"},
		taskID: "task-1", agentName: "external",
	}
	event := run.acpUpdateEvent(envelope, time.Unix(1, 0))
	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatal("acpUpdateEvent() = nil, want tool event")
	}
	got := metautil.String(update.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName)
	if got != "SendMessage" {
		t.Fatalf("runtime tool name = %q, want external UI-only presentation identity preserved", got)
	}
}

func TestRunnerQuiesceWaitsForCompletionPublication(t *testing.T) {
	t.Parallel()

	completionStarted := make(chan struct{})
	releaseCompletion := make(chan struct{})
	run := &childRun{
		anchor: delegation.Anchor{TaskID: "task-quiesce", SessionID: "child-quiesce", Agent: "helper"},
		taskID: "task-quiesce", state: delegation.StateRunning, running: true,
		done: make(chan struct{}), completion: completionSinkFunc(func(delegation.Result) {
			close(completionStarted)
			<-releaseCompletion
		}),
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{run.taskID: run}}
	go runner.finishDrive(context.Background(), run, "end_turn", nil)
	<-completionStarted

	quiesceDone := make(chan error, 1)
	go func() { quiesceDone <- runner.Quiesce(context.Background()) }()
	select {
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned before completion publication: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseCompletion)
	select {
	case err := <-quiesceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Quiesce did not return after completion publication")
	}
}

func TestRunnerQuiesceSignalsEveryChildBeforeWaiting(t *testing.T) {
	t.Parallel()

	firstCancelled := make(chan struct{})
	secondCancelled := make(chan struct{})
	first := &childRun{
		anchor: delegation.Anchor{TaskID: "task-first"}, taskID: "task-first",
		running: true, done: make(chan struct{}), cancel: func() { close(firstCancelled) },
	}
	second := &childRun{
		anchor: delegation.Anchor{TaskID: "task-second"}, taskID: "task-second",
		running: true, done: make(chan struct{}), cancel: func() { close(secondCancelled) },
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{
		first.taskID: first, second.taskID: second,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := runner.Quiesce(ctx); err == nil {
		t.Fatal("Quiesce() error = nil, want unfinished children timeout")
	}
	for name, cancelled := range map[string]<-chan struct{}{
		"first": firstCancelled, "second": secondCancelled,
	} {
		select {
		case <-cancelled:
		default:
			t.Fatalf("%s child did not receive Host shutdown", name)
		}
	}
}

func TestRunnerResolvesTypedPlacementWithoutRegistryIdentity(t *testing.T) {
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", Command: "helper-acp"}})
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	runner, err := NewRunner(RunnerConfig{
		Registry: registry,
		PlacementResolver: func(_ context.Context, _ tasksubagent.SpawnContext, req delegation.TargetRequest) (AgentConfig, error) {
			called++
			if req.Target.Selector != "orbit" || req.Target.Placement.Model != "provider/model" {
				t.Fatalf("placement request = %#v", req.Target)
			}
			return AgentConfig{Name: "orbit", Command: "dynamic-acp"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.resolveSpawnConfig(context.Background(), tasksubagent.SpawnContext{}, delegation.TargetRequest{Target: delegation.Target{
		Selector: "orbit",
		Placement: delegation.Placement{
			Kind: delegation.PlacementModel, Model: "provider/model", ConfigFingerprint: "config-v1", Fingerprint: "placement-v1",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || got.Name != "orbit" || got.Command != "dynamic-acp" {
		t.Fatalf("resolved config = %#v, calls=%d", got, called)
	}
}

func TestRunnerResolvesConfiguredPlacementToHostedAdapter(t *testing.T) {
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", Command: "helper-acp"}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{
		Registry: registry,
		PlacementResolver: func(_ context.Context, _ tasksubagent.SpawnContext, _ delegation.TargetRequest) (AgentConfig, error) {
			return AgentConfig{Name: "codex", HostedAdapterID: "codex"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.resolveSpawnConfig(context.Background(), tasksubagent.SpawnContext{}, delegation.TargetRequest{Target: delegation.Target{
		Selector: "codex-demo",
		Placement: delegation.Placement{
			Kind: delegation.PlacementAgent, Agent: "codex", ConfigFingerprint: "config-v1", Fingerprint: "placement-v1",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "codex" || got.HostedAdapterID != "codex" || got.Command != "" {
		t.Fatalf("resolved config = %#v", got)
	}
}

func TestDetachedChildContextClearsParentRuntimeFence(t *testing.T) {
	parent := session.ContextWithRuntimeFence(
		context.WithValue(context.Background(), detachedChildContextMarker{}, "kept"),
		session.SessionFence{FenceID: "parent-fence", OwnerID: "parent-owner", FencingToken: 7},
	)
	parent, cancel := context.WithCancel(parent)
	child := detachedChildContext(parent)
	cancel()

	if guard := session.RuntimeMutationGuard(child); guard != (session.MutationGuard{}) {
		t.Fatalf("child RuntimeMutationGuard = %#v, want cleared parent fence", guard)
	}
	if got := child.Value(detachedChildContextMarker{}); got != "kept" {
		t.Fatalf("child marker = %#v, want preserved unrelated value", got)
	}
	if err := child.Err(); err != nil {
		t.Fatalf("detached child context error after parent cancellation = %v", err)
	}
}

func TestRunnerHandleUpdatePublishesChildStream(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor: delegation.Anchor{
			TaskID:    "task-1",
			SessionID: "child-1",
			Agent:     "self",
			AgentID:   "self-1",
		},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, _ := json.Marshal(client.TextChunk{Type: "text", Text: "child output"})

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: client.UpdateAgentMessage,
			Content:       raw,
			MessageID:     "msg-1",
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	})

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Ref.TaskID != "task-1" || got.Ref.SessionID != "child-1" || got.Text != "" || !got.Running {
		t.Fatalf("stream frame = %#v", got)
	}
	if got.Event == nil || got.Event.Type != session.EventTypeAssistant || got.Event.Text != "child output" {
		t.Fatalf("stream event = %#v, want assistant child output", got.Event)
	}
	if got.Event.Visibility != session.VisibilityUIOnly || session.IsCanonicalHistoryEvent(got.Event) {
		t.Fatalf("stream event visibility = %q, canonical=%v; want ui_only trace", got.Event.Visibility, session.IsCanonicalHistoryEvent(got.Event))
	}
	if got.Event.Scope == nil || got.Event.Scope.Participant.Kind != session.ParticipantKindSubagent || got.Event.Scope.Participant.DelegationID != "task-1" {
		t.Fatalf("stream event scope = %#v, want subagent task scope", got.Event.Scope)
	}
	if got.Event.Protocol == nil || got.Event.Protocol.Update == nil {
		t.Fatalf("stream event protocol = %#v, want protocol update", got.Event.Protocol)
	}
	if got.Event.Protocol.Update.MessageID != "msg-1" {
		t.Fatalf("Protocol.Update.MessageID = %q, want msg-1", got.Event.Protocol.Update.MessageID)
	}
	vendor, _ := got.Event.Protocol.Update.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("Protocol.Update.Meta = %#v, want vendor trace", got.Event.Protocol.Update.Meta)
	}
}

func TestTranslateApprovalRequestPreservesCanonicalToolPayload(t *testing.T) {
	t.Parallel()

	content := []client.ToolCallContent{{Type: "content", Content: client.TextContent{Type: "text", Text: "permission detail"}}}
	req := client.RequestPermissionRequest{
		SessionID: "child-1",
		ToolCall: client.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("write file"),
			Status:        stringPtr("pending"),
			RawInput:      map[string]any{"path": "a.txt"},
			RawOutput:     map[string]any{"preview": "new text"},
			Content:       content,
		},
		Options: []client.PermissionOption{{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"}},
	}

	got, err := translateApprovalRequest(tasksubagent.SpawnContext{TaskID: "task-1"}, AgentConfig{Name: "child"}, "child-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolCall.RawOutput["preview"] != "new text" {
		t.Fatalf("raw output = %#v, want preserved preview", got.ToolCall.RawOutput)
	}
	if len(got.ToolCall.Content) != 1 || schema.ExtractTextValue(got.ToolCall.Content[0].Content) != "permission detail" {
		t.Fatalf("content = %#v, want preserved canonical content", got.ToolCall.Content)
	}
}

func TestRunnerPermissionCallbackNormalizesChildApprovalWithoutPublishingFrame(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	runner := &Runner{}
	var captured tasksubagent.ApprovalRequest
	requester := subagentApprovalRequesterFunc(func(ctx context.Context, req tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
		captured = req
		return tasksubagent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	handler := runner.permissionCallback(tasksubagent.SpawnContext{
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		TaskID:            "task-1",
		ParentCallID:      "spawn-call-1",
		ApprovalRequester: requester,
		Streams:           sink,
	}, AgentConfig{Name: "helper"}, "helper-1")

	response, err := handler(context.Background(), client.RequestPermissionRequest{
		SessionID: "child-session",
		ToolCall: client.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "child-call-1",
			Kind:          stringPtr("edit"),
			Title:         stringPtr("Write child file"),
			Status:        stringPtr("pending"),
			RawInput:      map[string]any{"path": "child.txt"},
			RawOutput:     map[string]any{"preview": "new text"},
			Content: []client.ToolCallContent{{
				Type:    "content",
				Content: client.TextContent{Type: "text", Text: "child permission detail"},
			}},
		},
		Options: []client.PermissionOption{{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
	})
	if err != nil {
		t.Fatalf("permission callback error = %v", err)
	}
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_once" {
		t.Fatalf("permission callback response = %#v, want selected allow_once", response)
	}
	if len(sink.frames) != 0 {
		t.Fatalf("permission frames = %#v, want no child-owned permission frame", sink.frames)
	}
	if captured.TaskID != "task-1" || captured.ParentCallID != "spawn-call-1" || captured.Agent != "helper" {
		t.Fatalf("normalized child approval = %#v, want inherited child origin", captured)
	}
	if captured.ToolCall.ID != "child-call-1" || captured.ToolCall.RawInput["path"] != "child.txt" || captured.ToolCall.RawOutput["preview"] != "new text" || len(captured.ToolCall.Content) != 1 || len(captured.Options) != 1 {
		t.Fatalf("normalized child approval payload = %#v, want original ACP tool payload", captured)
	}
}

func TestRunnerCancelReturnsRemoteNotificationFailure(t *testing.T) {
	t.Parallel()

	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	remote, err := client.NewStreamClient(stdinWriter, stdoutReader, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = stdinReader.Close()
	defer stdoutWriter.Close()
	defer remote.Close(context.Background())
	localCancelled := false
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-cancel", SessionID: "child-cancel", Agent: "helper", AgentID: "helper-1"},
		client:  remote,
		state:   delegation.StateRunning,
		running: true,
		cancel:  func() { localCancelled = true },
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{"task-cancel": run}}

	err = runner.Cancel(context.Background(), run.anchor)
	if err == nil {
		t.Fatal("Cancel() error = nil, want remote notification failure")
	}
	if !localCancelled {
		t.Fatal("local child context was not cancelled after remote failure")
	}
	run.mu.RLock()
	state := run.state
	run.mu.RUnlock()
	if state == delegation.StateCancelled {
		t.Fatalf("child state = %q, must not claim cancellation after remote failure", state)
	}
}

func TestRunnerCancelNaturalTerminalIsEndpointNoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		state      delegation.State
		finishing  bool
		result     string
		diagnostic string
	}{
		{name: "completed", state: delegation.StateCompleted, result: "natural final"},
		{name: "failed finishing", state: delegation.StateFailed, finishing: true, diagnostic: "natural failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stdoutReader, stdoutWriter := io.Pipe()
			stdinReader, stdinWriter := io.Pipe()
			remote, err := client.NewStreamClient(stdinWriter, stdoutReader, client.Config{})
			if err != nil {
				t.Fatal(err)
			}
			_ = stdinReader.Close()
			defer stdoutWriter.Close()
			defer remote.Close(context.Background())
			localCancelled := false
			run := &childRun{
				anchor: delegation.Anchor{
					TaskID: "task-natural-" + test.name, SessionID: "child-natural", AgentID: "helper-1",
				},
				taskID: "task-natural-" + test.name, client: remote,
				state: test.state, result: test.result, failureDetail: test.diagnostic,
				finishing: test.finishing, cancel: func() { localCancelled = true },
			}
			runner := &Runner{clock: time.Now, runs: map[string]*childRun{run.taskID: run}}

			if err := runner.Cancel(context.Background(), run.anchor); err != nil {
				t.Fatalf("Cancel() error = %v, want terminal endpoint no-op", err)
			}
			run.mu.RLock()
			cancelRequested := run.cancelRequested
			state := run.state
			finishing := run.finishing
			run.mu.RUnlock()
			if cancelRequested || localCancelled || state != test.state || finishing != test.finishing {
				t.Fatalf("terminal run after Cancel = state %q finishing=%v cancelRequested=%v localCancelled=%v", state, finishing, cancelRequested, localCancelled)
			}
			observed, err := runner.Wait(context.Background(), run.anchor, 0)
			if err != nil || observed.Running || observed.State != test.state || observed.Result != test.result || observed.Error != test.diagnostic {
				t.Fatalf("Wait() = %#v, %v; want original terminal result", observed, err)
			}
		})
	}
}

func TestDelegationTerminalStatesRemainEligibleForAnotherTurn(t *testing.T) {
	t.Parallel()

	for _, state := range []delegation.State{
		delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome,
	} {
		if !delegationStateCanStartTurn(state) {
			t.Fatalf("delegationStateCanStartTurn(%q) = false, want idle child to remain resumable", state)
		}
	}
	for _, state := range []delegation.State{delegation.StateRunning, delegation.StateWaitingApproval, ""} {
		if delegationStateCanStartTurn(state) {
			t.Fatalf("delegationStateCanStartTurn(%q) = true, want active/incomplete Turn rejected", state)
		}
	}
}

func TestRunnerHandleUpdatePublishesStructuredToolAndPlanEvents(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "call-1",
			Kind:          "execute",
			Title:         "run go test",
			Status:        "pending",
			RawInput:      map[string]any{"command": "go test ./surfaces/tui/app/..."},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("run go test"),
			Status:        stringPtr("completed"),
			RawInput:      map[string]any{"command": "go test ./surfaces/tui/app/..."},
			RawOutput:     map[string]any{"stdout": "ok\n", "exit_code": 0},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.PlanUpdate{
			SessionUpdate: client.UpdatePlan,
			Entries:       []client.PlanEntry{{Content: "Run tests", Status: "completed"}},
		},
	})

	if got := len(sink.frames); got != 3 {
		t.Fatalf("stream frames = %#v, want three structured updates", sink.frames)
	}
	for i, frame := range sink.frames {
		if frame.Text != "" {
			t.Fatalf("structured frame %d text = %q, want event-only frame", i, frame.Text)
		}
	}
	callEvent := sink.frames[0].Event
	callUpdate := session.ProtocolUpdateOf(callEvent)
	if callEvent == nil || callEvent.Type != session.EventTypeToolCall || callUpdate == nil {
		t.Fatalf("tool call event = %#v", callEvent)
	}
	if callEvent.Visibility != session.VisibilityUIOnly || session.IsCanonicalHistoryEvent(callEvent) {
		t.Fatalf("tool call visibility = %q, canonical=%v; want ui_only trace", callEvent.Visibility, session.IsCanonicalHistoryEvent(callEvent))
	}
	if callUpdate.Title != "run go test" || callUpdate.Kind != "execute" || callUpdate.RawInput["command"] != "go test ./surfaces/tui/app/..." {
		t.Fatalf("tool call payload = %#v", callUpdate)
	}
	resultEvent := sink.frames[1].Event
	resultUpdate := session.ProtocolUpdateOf(resultEvent)
	if resultEvent == nil || resultEvent.Type != session.EventTypeToolResult || resultUpdate == nil {
		t.Fatalf("tool result event = %#v", resultEvent)
	}
	if resultEvent.Visibility != session.VisibilityUIOnly || session.IsCanonicalHistoryEvent(resultEvent) {
		t.Fatalf("tool result visibility = %q, canonical=%v; want ui_only trace", resultEvent.Visibility, session.IsCanonicalHistoryEvent(resultEvent))
	}
	if resultUpdate.RawOutput["stdout"] != "ok\n" {
		t.Fatalf("tool result payload = %#v", resultUpdate)
	}
	planEvent := sink.frames[2].Event
	planUpdate := session.ProtocolUpdateOf(planEvent)
	if planEvent == nil || planEvent.Type != session.EventTypePlan || planUpdate == nil {
		t.Fatalf("plan event = %#v", planEvent)
	}
	if planEvent.Visibility != session.VisibilityUIOnly || session.IsCanonicalHistoryEvent(planEvent) {
		t.Fatalf("plan visibility = %q, canonical=%v; want ui_only trace", planEvent.Visibility, session.IsCanonicalHistoryEvent(planEvent))
	}
	if len(planUpdate.Entries) != 1 || planUpdate.Entries[0].Content != "Run tests" {
		t.Fatalf("plan entries = %#v", planUpdate.Entries)
	}
}

func TestRunnerRestoresGrokExecutePresentationForSpawnStream(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "grok", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "execute-1",
			Title:         "run_terminal_command",
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "git status --short"},
			Meta: map[string]any{"x.ai/tool": map[string]any{
				"version": 1, "name": "run_terminal_command", "kind": "execute",
				"namespace": "grok_build", "label": "Run Command", "read_only": false,
			}},
		},
	})

	if len(sink.frames) != 1 || sink.frames[0].Event == nil {
		t.Fatalf("Spawn stream frames = %#v, want one Grok execute event", sink.frames)
	}
	update := session.ProtocolUpdateOf(sink.frames[0].Event)
	if update == nil || update.Kind != schema.ToolKindExecute || update.Title != "run_terminal_command" {
		t.Fatalf("Spawn Grok execute update = %#v, want anonymous standard execute presentation", update)
	}
	if exactName := metautil.String(update.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); exactName != "" {
		t.Fatalf("Spawn runtime exact tool name = %q, want none", exactName)
	}
}

func TestRunnerDoesNotInventBuiltinIdentityForGenericFetch(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "codex", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "ws_1",
			Kind:          stringPtr("fetch"),
			Title:         stringPtr("Searching for: weather: Shanghai, China"),
			Status:        stringPtr("in_progress"),
			RawInput:      map[string]any{"query": "weather: Shanghai, China"},
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one structured search update", sink.frames)
	}
	frame := sink.frames[0]
	if frame.Text != "" {
		t.Fatalf("stream frame text = %q, want event-only child tool update", frame.Text)
	}
	event := frame.Event
	update := session.ProtocolUpdateOf(event)
	if event == nil || update == nil {
		t.Fatalf("stream event = %#v, want structured tool call", event)
	}
	if got := update.Title; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool title = %q, want ACP title", got)
	}
	if got := update.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := update.RawInput["query"]; got != "weather: Shanghai, China" {
		t.Fatalf("raw input query = %#v", got)
	}
}

func TestRunnerPreservesChildTerminalContentWithoutParentTraceText(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "command-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("run date loop"),
			Content: []client.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "command-1",
				Content:    client.TextContent{Type: "text", Text: "17:21:17\n"},
			}},
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one terminal output frame", sink.frames)
	}
	if got := sink.frames[0].Text; got != "" {
		t.Fatalf("stream frame text = %q, want child terminal result omitted from parent SPAWN trace", got)
	}
	event := sink.frames[0].Event
	if event == nil || event.Protocol == nil || event.Protocol.Update == nil ||
		len(session.ProtocolToolCallContentOf(event.Protocol.Update)) == 0 {
		t.Fatalf("stream event = %#v, want structured terminal content preserved", event)
	}
}

func TestRunnerStripsConsoleFenceFromChildTerminalOutput(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "codex", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	fenced := "```console\ndiff --git a/file b/file\n```\n"
	want := "diff --git a/file b/file\n"

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "command-1",
			Kind:          stringPtr("execute"),
			Title:         stringPtr("Ran git diff HEAD -- file | head -300"),
			Status:        stringPtr("completed"),
			RawOutput:     map[string]any{"stdout": fenced},
			Content: []client.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "command-1",
				Content:    client.TextContent{Type: "text", Text: fenced},
			}},
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one terminal output frame", sink.frames)
	}
	frame := sink.frames[0]
	if frame.Text != "" {
		t.Fatalf("stream frame text = %q, want child terminal result omitted from parent SPAWN trace", frame.Text)
	}
	event := frame.Event
	update := session.ProtocolUpdateOf(event)
	if event == nil || update == nil {
		t.Fatalf("stream event = %#v, want structured tool result event", event)
	}
	if got := update.RawOutput["stdout"]; got != fenced {
		t.Fatalf("raw output stdout = %#v, want original %q", got, fenced)
	}
	content := session.ProtocolToolCallContentOf(update)
	if got := schema.ExtractTextValue(content[0].Content); got != want {
		t.Fatalf("terminal content = %q, want %q", got, want)
	}
}

func TestRunnerHandleUpdateUsesAgentMessageDeltas(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateUserMessage, "continue from parent"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我来按步骤"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "执行"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "这个任务。"))

	if got := len(sink.frames); got != 4 {
		t.Fatalf("stream frames = %#v, want input plus three agent delta updates", sink.frames)
	}
	input := sink.frames[0].Event
	if input == nil || session.EventTypeOf(input) != session.EventTypeContext ||
		input.Visibility != session.VisibilityUIOnly || input.Actor.Kind != session.ActorKindController ||
		input.Actor.Name != "parent" || input.Text != "continue from parent" {
		t.Fatalf("child input event = %#v, want typed UI-only parent Context", input)
	}
	var renderedEvents string
	for _, frame := range sink.frames[1:] {
		if frame.Text != "" {
			t.Fatalf("stream frame text = %q, want event-only child update", frame.Text)
		}
		if frame.Event == nil {
			t.Fatalf("stream frame event = nil, want structured delta event")
		}
		renderedEvents += frame.Event.Text
	}
	if renderedEvents != "我来按步骤执行这个任务。" {
		t.Fatalf("rendered event stream = %q, want deduped final text", renderedEvents)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "我来按步骤执行这个任务。" {
		t.Fatalf("run.result = %q, want deduped final text", result)
	}
}

func TestRunnerHandleUpdateKeepsTrustedAgentCommunicationSource(t *testing.T) {
	t.Parallel()

	source := session.ActorRef{
		Kind: session.ActorKindParticipant, ID: "reviewer-1", Role: "delegated", Name: "reviewer",
	}
	sink := &recordingStreams{}
	run := &childRun{
		anchor: delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID: "task-1", sink: sink, state: delegation.StateRunning, running: true,
		inputActor: source,
	}
	runner := &Runner{clock: time.Now}
	input := session.AgentCommunicationPromptHeader(source) + "\nreview this change"

	runner.handleUpdate(run, contentUpdate(t, client.UpdateUserMessage, input))

	if len(sink.frames) != 1 || sink.frames[0].Event == nil {
		t.Fatalf("stream frames = %#v, want one Agent communication", sink.frames)
	}
	event := sink.frames[0].Event
	communication := session.ProtocolAgentCommunicationOf(event)
	if session.EventTypeOf(event) != session.EventTypeContext || event.Actor != source ||
		communication == nil || communication.Text != "review this change" ||
		strings.Contains(communication.Text, "[Internal agent message]") {
		t.Fatalf("child input event = %#v, want trusted sibling identity and raw body", event)
	}
}

func TestRunnerHandleUpdatePublishesRepeatedAgentMessageDeltas(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "ha"))
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "ha"))

	if got := len(sink.frames); got != 2 {
		t.Fatalf("stream frames = %#v, want both repeated ACP deltas", sink.frames)
	}
	var rendered string
	for i, frame := range sink.frames {
		if frame.Event == nil || frame.Event.Text != "ha" {
			t.Fatalf("stream frame %d = %#v, want exact ha delta", i, frame)
		}
		if update := session.ProtocolUpdateOf(frame.Event); update == nil || update.MessageID != "" {
			t.Fatalf("stream frame %d update = %#v, want real Grok message ID omission preserved", i, update)
		}
		rendered += frame.Event.Text
	}
	if rendered != "haha" {
		t.Fatalf("rendered event stream = %q, want haha", rendered)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "haha" {
		t.Fatalf("run.result = %q, want exact repeated deltas", result)
	}
}

func TestRunnerHandleUpdateSeparatesAgentMessageIDs(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdateWithMessageID(t, client.UpdateAgentMessage, "m1", "first message"))
	runner.handleUpdate(run, contentUpdateWithMessageID(t, client.UpdateAgentMessage, "m2", "second message"))

	if got := len(sink.frames); got != 2 {
		t.Fatalf("stream frames = %#v, want two agent updates", sink.frames)
	}
	if sink.frames[0].Text != "" || sink.frames[1].Text != "" {
		t.Fatalf("stream texts = %q / %q, want event-only updates", sink.frames[0].Text, sink.frames[1].Text)
	}
	if sink.frames[0].Event == nil || sink.frames[0].Event.Text != "first message" || sink.frames[1].Event == nil || sink.frames[1].Event.Text != "second message" {
		t.Fatalf("stream events = %#v / %#v, want message-id separated chunks", sink.frames[0].Event, sink.frames[1].Event)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "second message" {
		t.Fatalf("run.result = %q, want latest message-id segment", result)
	}
}

func TestRunnerResultKeepsOnlyLatestAssistantSegmentAfterTools(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "我先读取文件。"))
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "read-1",
			Kind:          "read",
			Title:         "Read hello_spawn.txt",
			Status:        "in_progress",
			RawInput:      map[string]any{"path": "hello_spawn.txt"},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "read-1",
			Kind:          stringPtr("read"),
			Title:         stringPtr("Read hello_spawn.txt"),
			Status:        stringPtr("completed"),
		},
	})
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "总结一下执行结果：\n步骤 操作 结果"))

	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "总结一下执行结果：\n步骤 操作 结果" {
		t.Fatalf("run.result = %q, want latest assistant segment only", result)
	}
	for _, frame := range sink.frames {
		if frame.Text != "" {
			t.Fatalf("stream frame text = %q, want event-only child updates", frame.Text)
		}
	}
}

func TestRunnerPublishesSemanticFramesWithoutFormattedTrace(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "先看一下文件"))
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "list-1",
			Kind:          "search",
			Title:         "LIST /Users/xueyongzhi/WorkDir/xueyongzhi/demo",
			Status:        "in_progress",
			RawInput:      map[string]any{"path": "/Users/xueyongzhi/WorkDir/xueyongzhi/demo"},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "list-1",
			Kind:          stringPtr("search"),
			Title:         stringPtr("LIST /Users/xueyongzhi/WorkDir/xueyongzhi/demo"),
			Status:        stringPtr("completed"),
			RawInput:      map[string]any{"path": "/Users/xueyongzhi/WorkDir/xueyongzhi/demo"},
		},
	})
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "glob-1",
			Kind:          "search",
			Title:         "GLOB **/*.md in /Users/xueyongzhi/WorkDir/xueyongzhi/demo",
			Status:        "in_progress",
			RawInput:      map[string]any{"pattern": "**/*.md", "path": "/Users/xueyongzhi/WorkDir/xueyongzhi/demo"},
		},
	})

	if got := len(sink.frames); got != 4 {
		t.Fatalf("stream frames = %#v, want assistant, tool call, completed event, tool call", sink.frames)
	}
	for i, frame := range sink.frames {
		if frame.Text != "" || frame.Event == nil {
			t.Fatalf("frame %d = %#v, want event-only semantic frame", i, frame)
		}
	}
}

func TestRunnerResultDoesNotUseToolPreviewAsFinalAnswer(t *testing.T) {
	t.Parallel()

	run := &childRun{
		anchor:        delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "claude", AgentID: "agent-1"},
		taskID:        "task-1",
		state:         delegation.StateCompleted,
		outputPreview: "Read hello_spawn.txt completed",
		running:       false,
		updatedAt:     time.Now(),
	}
	runner := &Runner{clock: time.Now}

	got := runner.waitRun(context.Background(), run, 0)
	if got.Result != "" {
		t.Fatalf("Result = %q, want empty final answer for tool-only run", got.Result)
	}
	if got.OutputPreview != "Read hello_spawn.txt completed" {
		t.Fatalf("OutputPreview = %q, want preview preserved", got.OutputPreview)
	}
}

func TestRunnerAgentMessageDeltaMergeDoesNotUseOverlapHeuristic(t *testing.T) {
	t.Parallel()

	run := &childRun{}
	if got := run.appendAgentMessageLocked("abcabc"); got != "abcabc" {
		t.Fatalf("first delta = %q, want abcabc", got)
	}
	if got := run.appendAgentMessageLocked("abcXYZ"); got != "abcXYZ" {
		t.Fatalf("overlapping delta = %q, want full incoming chunk", got)
	}
	if run.result != "abcabcabcXYZ" {
		t.Fatalf("run.result = %q, want exact appended chunks", run.result)
	}

	prefixGrowing := &childRun{}
	if got := prefixGrowing.appendAgentMessageLocked("a"); got != "a" {
		t.Fatalf("first prefix-growing delta = %q, want a", got)
	}
	if got := prefixGrowing.appendAgentMessageLocked("ab"); got != "ab" {
		t.Fatalf("second prefix-growing delta = %q, want exact ab", got)
	}
	if prefixGrowing.result != "aab" {
		t.Fatalf("prefix-growing run.result = %q, want aab", prefixGrowing.result)
	}
}

func TestRunnerAgentMessageDeltaMergePreservesMixedLanguageChunks(t *testing.T) {
	t.Parallel()

	chunks := []string{
		"Let me quickly inspect these files to understand the project.",
		"\n\nNow I can summarize: ",
		"这是一个包含 calc 和 hello 模块的 Python 项目，覆盖主模块功能和边界测试。",
	}
	run := &childRun{}
	var rendered string
	for _, chunk := range chunks {
		rendered += run.appendAgentMessageLocked(chunk)
	}
	want := strings.Join(chunks, "")
	if rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
	if run.result != want {
		t.Fatalf("run.result = %q, want %q", run.result, want)
	}
}

func TestRunnerHandleUpdatePublishesStructuredThoughtEvent(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, "thinking about the command"))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Text != "" {
		t.Fatalf("stream frame text = %q, want event-only thought update", got.Text)
	}
	update := session.ProtocolUpdateOf(got.Event)
	if got.Event == nil || update == nil || update.SessionUpdate != client.UpdateAgentThought || got.Event.Text != "thinking about the command" {
		t.Fatalf("stream event = %#v, want structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdatePreservesWhitespaceThoughtChunk(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "agent-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, " "))

	if len(sink.frames) != 1 {
		t.Fatalf("stream frames = %#v, want one whitespace thought frame", sink.frames)
	}
	got := sink.frames[0]
	if got.Text != "" {
		t.Fatalf("stream frame text = %q, want structured thought event only", got.Text)
	}
	if got.Event == nil || got.Event.Text != " " {
		t.Fatalf("stream event = %#v, want single-space structured thought event", got.Event)
	}
}

func TestRunnerHandleUpdateDoesNotHoldRunLockWhilePublishing(t *testing.T) {
	t.Parallel()

	sink := &blockingStreams{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "self", AgentID: "self-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}

	done := make(chan struct{})
	go func() {
		runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentMessage, "blocked output"))
		close(done)
	}()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("PublishStream was not called")
	}

	locked := make(chan struct{})
	go func() {
		run.mu.Lock()
		run.outputPreview = "lock was available"
		run.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("run lock stayed held while PublishStream was blocked")
	}

	close(sink.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleUpdate did not return after releasing PublishStream")
	}
}

func TestChildRunKeyIsolatesSharedRemoteSessionIDs(t *testing.T) {
	t.Parallel()

	runner := &Runner{clock: time.Now, runs: map[string]*childRun{}}
	a := &childRun{anchor: delegation.Anchor{TaskID: "task-a", SessionID: "session-1", Agent: "helper", AgentID: "task-a"}}
	b := &childRun{anchor: delegation.Anchor{TaskID: "task-b", SessionID: "session-1", Agent: "helper", AgentID: "task-b"}}
	keyA, err := childRunKey(a.anchor)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := childRunKey(b.anchor)
	if err != nil {
		t.Fatal(err)
	}
	if keyA == keyB {
		t.Fatalf("child run keys collided for shared remote session ids: %q", keyA)
	}
	runner.runs[keyA] = a
	runner.runs[keyB] = b

	gotA, err := runner.lookup(a.anchor)
	if err != nil || gotA != a {
		t.Fatalf("lookup(A) = %#v, %v", gotA, err)
	}
	gotB, err := runner.lookup(b.anchor)
	if err != nil || gotB != b {
		t.Fatalf("lookup(B) = %#v, %v", gotB, err)
	}
	if _, err := runner.lookup(delegation.Anchor{TaskID: "task-a", SessionID: "session-other"}); err == nil {
		t.Fatal("lookup with mismatched session_id succeeded, want isolation error")
	}
}

func TestStableAgentIDUsesDurableTaskID(t *testing.T) {
	t.Parallel()

	runner := &Runner{}
	if got := runner.stableAgentID("helper", "spawn-task-42"); got != "spawn-task-42" {
		t.Fatalf("stableAgentID = %q, want durable task id", got)
	}
	first := runner.stableAgentID("helper", "")
	second := runner.stableAgentID("helper", "")
	if first == "" || first == second {
		t.Fatalf("fallback agent ids = %q/%q, want distinct counter values", first, second)
	}
}

func TestRunnerHandleUpdateAcceptsStringContentChunks(t *testing.T) {
	t.Parallel()

	sink := &recordingStreams{}
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-1", SessionID: "child-1", Agent: "copilot", AgentID: "copilot-1"},
		taskID:  "task-1",
		sink:    sink,
		state:   delegation.StateRunning,
		running: true,
	}
	runner := &Runner{clock: time.Now}
	raw, err := json.Marshal("string chunk")
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: client.UpdateAgentMessage,
			Content:       raw,
		},
	})

	if got := len(sink.frames); got != 1 {
		t.Fatalf("stream frames = %#v, want one string-content frame", sink.frames)
	}
	if got := sink.frames[0].Text; got != "" {
		t.Fatalf("stream frame text = %q, want event-only string chunk", got)
	}
	if sink.frames[0].Event == nil || sink.frames[0].Event.Text != "string chunk" {
		t.Fatalf("stream event = %#v, want string chunk", sink.frames[0].Event)
	}
	run.mu.RLock()
	result := run.result
	run.mu.RUnlock()
	if result != "string chunk" {
		t.Fatalf("run.result = %q, want string chunk", result)
	}
}

func contentUpdate(t *testing.T, kind string, text string) client.UpdateEnvelope {
	t.Helper()
	return contentUpdateWithMessageID(t, kind, "", text)
}

func contentUpdateWithMessageID(t *testing.T, kind string, messageID string, text string) client.UpdateEnvelope {
	t.Helper()
	raw, err := json.Marshal(client.TextChunk{Type: "text", Text: text})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ContentChunk{
			SessionUpdate: kind,
			Content:       raw,
			MessageID:     messageID,
		},
	}
}

func stringPtr(value string) *string {
	return &value
}

type recordingStreams struct {
	frames []stream.Frame
}

type subagentApprovalRequesterFunc func(context.Context, tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error)

func (f subagentApprovalRequesterFunc) RequestSubagentApproval(ctx context.Context, req tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
	return f(ctx, req)
}

func (s *recordingStreams) PublishStream(frame stream.Frame) {
	s.frames = append(s.frames, stream.CloneFrame(frame))
}

type blockingStreams struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingStreams) PublishStream(stream.Frame) {
	close(s.entered)
	<-s.release
}

func repoRootForRunnerTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}
