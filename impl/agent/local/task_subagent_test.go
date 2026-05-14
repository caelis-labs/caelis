package local

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	"github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestSlashSideSubagentReceivesSharedContextAndPublishesPublicDialogue(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "review result"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	userMessage := model.NewTextMessage(model.RoleUser, "previous request")
	assistantMessage := model.NewTextMessage(model.RoleAssistant, "previous answer")
	for _, event := range []*session.Event{{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &userMessage,
		Text:       "previous request",
	}, {
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &assistantMessage,
		Text:       "previous answer",
	}} {
		if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: activeSession.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	snapshot, err := runtime.StartSubagent(ctx, activeSession.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if snapshot.State != task.StateCompleted {
		t.Fatalf("snapshot state = %q, want completed", snapshot.State)
	}
	if prompt := runner.spawnRequest.Prompt; !strings.Contains(prompt, "shared_dialogue_delta:") ||
		!strings.Contains(prompt, "[1] user:\nprevious request") ||
		!strings.Contains(prompt, "[2] assistant:\nprevious answer") ||
		!strings.Contains(prompt, "Current request:\nreview") {
		t.Fatalf("spawn prompt missing shared side context:\n%s", prompt)
	} else if strings.Contains(prompt, "current_user_request") {
		t.Fatalf("spawn prompt duplicated current request in context prelude:\n%s", prompt)
	}

	loaded, err := runtime.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sideUser, sideAssistant *session.Event
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || event.Scope.Participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			sideUser = event
		case session.EventTypeAssistant:
			sideAssistant = event
		}
	}
	if sideUser == nil || strings.TrimSpace(sideUser.Text) != "review" || !session.IsMainInvocationVisibleEvent(sideUser) {
		t.Fatalf("side user event = %#v, want public review request", sideUser)
	}
	if sideAssistant == nil || strings.TrimSpace(sideAssistant.Text) != "review result" || !session.IsMainInvocationVisibleEvent(sideAssistant) {
		t.Fatalf("side assistant event = %#v, want public final result", sideAssistant)
	}
	updated, err := runtime.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if len(updated.Participants) != 1 || updated.Participants[0].Role != session.ParticipantRoleSidecar || updated.Participants[0].ContextSyncSeq == 0 {
		t.Fatalf("participants = %#v, want sidecar subagent with context checkpoint", updated.Participants)
	}
}

func TestSubagentTaskIDForHandleAllowsSidecarCustomSource(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		Participants: []session.ParticipantBinding{{
			ID:           "side-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleSidecar,
			Label:        "@jeff",
			Source:       "custom_codex",
			DelegationID: "task-side",
		}, {
			ID:           "delegated-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			Label:        "@jude",
			Source:       "agent_spawn",
			DelegationID: "task-delegated",
		}},
	}
	taskID, binding, ok := subagentTaskIDForHandle(activeSession, "jeff")
	if !ok || taskID != "task-side" || binding.ID != "side-1" {
		t.Fatalf("subagentTaskIDForHandle(sidecar) = (%q, %#v, %v), want custom-source sidecar", taskID, binding, ok)
	}
	if _, _, ok := subagentTaskIDForHandle(activeSession, "jude"); ok {
		t.Fatal("subagentTaskIDForHandle(delegated) = ok, want hidden from @handle")
	}
}

func TestAllocateSubagentHandleUsesAgentDerivedFallback(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{Participants: []session.ParticipantBinding{
		{Label: "@codex"},
		{Label: "@codex2"},
	}}
	if got := allocateSubagentHandle(activeSession, "codex"); got != "codex3" {
		t.Fatalf("allocateSubagentHandle() = %q, want codex3", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "Anthropic/Claude Agent"); got != "anthropic-claude-agent" {
		t.Fatalf("allocateSubagentHandle() = %q, want normalized agent handle", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "!!!"); got != "agent" {
		t.Fatalf("allocateSubagentHandle() = %q, want generic fallback", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "self"); got != "jeff" {
		t.Fatalf("allocateSubagentHandle(self) = %q, want named handle", got)
	}
	usedSelfHandle := session.Session{Participants: []session.ParticipantBinding{{Label: "@jeff"}}}
	if got := allocateSubagentHandle(usedSelfHandle, "self"); got != "emma" {
		t.Fatalf("allocateSubagentHandle(self with used handle) = %q, want next named handle", got)
	}
}

func TestStartSubagentAllocatesUniqueHandlesFromRuntimeReservations(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	first, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent(first) error = %v", err)
	}
	second, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "second",
	})
	if err != nil {
		t.Fatalf("StartSubagent(second) error = %v", err)
	}
	if got := taskStringValue(first.Result["handle"]); got != "helper" {
		t.Fatalf("first handle = %q, want helper", got)
	}
	if got := taskStringValue(second.Result["handle"]); got != "helper2" {
		t.Fatalf("second handle = %q, want helper2", got)
	}
}

func TestTaskToolResultEventMetaMarksSubagentWriteTarget(t *testing.T) {
	t.Parallel()

	meta := taskToolResultEventMeta(nil, "write", "请追加两行", task.Snapshot{
		Ref:  task.Ref{TaskID: "task-1"},
		Kind: task.KindSubagent,
		Result: map[string]any{
			"handle": "maya",
		},
	})
	caelis, ok := meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want caelis extension", meta)
	}
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	targetTool, _ := runtimeMeta["tool"].(map[string]any)
	if targetTool["name"] != "TASK" || targetTool["action"] != "write" || targetTool["target_kind"] != "subagent" || targetTool["target_id"] != "maya" || targetTool["input"] != "请追加两行" {
		t.Fatalf("runtime.tool = %#v, want TASK write subagent target", targetTool)
	}
}

func TestTaskWriteContinuesCompletedSpawnChild(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first done"},
		continueResult: delegation.Result{
			State:  delegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if started.State != task.StateCompleted {
		t.Fatalf("started state = %q, want completed", started.State)
	}

	continued, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "next prompt",
	})
	if err != nil {
		t.Fatalf("Write(completed spawn) error = %v", err)
	}
	if got, _ := continued.Result["result"].(string); got != "follow-up done" {
		t.Fatalf("continued result = %q, want follow-up done", got)
	}
	if runner.continuePrompt != "next prompt" {
		t.Fatalf("continue prompt = %q, want next prompt", runner.continuePrompt)
	}
	if runner.continueAnchor.TaskID != started.Ref.TaskID {
		t.Fatalf("continue anchor task id = %q, want %q", runner.continueAnchor.TaskID, started.Ref.TaskID)
	}
	if continued.StdoutCursor != int64(len("follow-up done")) {
		t.Fatalf("continued stdout cursor = %d, want only follow-up output length", continued.StdoutCursor)
	}
	if got, want := taskStringValue(continued.Result["turn_id"]), started.Ref.TaskID+":2"; got != want {
		t.Fatalf("continued turn_id = %q, want %q", got, want)
	}
}

func TestSideAndDelegatedSubagentsHaveSeparateControlSurfaces(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:    delegation.Result{State: delegation.StateCompleted, Result: "side done"},
		continueResult: delegation.Result{State: delegation.StateCompleted, Result: "continued"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	side, err := runtime.StartSubagent(ctx, activeSession.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent(side) error = %v", err)
	}
	if _, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: taskStringValue(side.Result["handle"]),
		Source: "agent_tool",
	}); err == nil || !strings.Contains(err.Error(), "cannot control user-created side subagent") {
		t.Fatalf("TASK wait on side err = %v, want isolation error", err)
	}
	if _, err := runtime.ContinueSubagentByHandle(ctx, activeSession.SessionRef, taskStringValue(side.Result["handle"]), "follow up", 0); err != nil {
		t.Fatalf("ContinueSubagentByHandle(side) error = %v", err)
	}
	if !strings.Contains(runner.continuePrompt, "shared_dialogue_delta:\n(none)") || !strings.Contains(runner.continuePrompt, "Current request:\nfollow up") {
		t.Fatalf("side continuation prompt missing shared context:\n%s", runner.continuePrompt)
	}
	if strings.Contains(runner.continuePrompt, "current_user_request") {
		t.Fatalf("side continuation duplicated current request in context prelude:\n%s", runner.continuePrompt)
	}
	if strings.Contains(runner.continuePrompt, "side done") {
		t.Fatalf("side continuation repeated prior side final output:\n%s", runner.continuePrompt)
	}

	updatedSession, err := runtime.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	delegated, err := runtime.tasks.StartSubagent(ctx, updatedSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "internal task",
	})
	if err != nil {
		t.Fatalf("StartSubagent(delegated) error = %v", err)
	}
	if _, err := runtime.ContinueSubagentByHandle(ctx, activeSession.SessionRef, taskStringValue(delegated.Result["handle"]), "user follow up", 0); err == nil {
		t.Fatal("ContinueSubagentByHandle(delegated) succeeded, want not found")
	}
}

func TestTaskWriteContinuesSpawnChildAfterWaitCompletes(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, OutputPreview: "working", Running: true},
		waitResult:  delegation.Result{State: delegation.StateCompleted, Result: "first done"},
		continueResult: delegation.Result{
			State:  delegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	completed, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
	})
	if err != nil {
		t.Fatalf("Wait(spawn) error = %v", err)
	}
	if completed.State != task.StateCompleted {
		t.Fatalf("completed state = %q, want completed", completed.State)
	}

	continued, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "next prompt",
	})
	if err != nil {
		t.Fatalf("Write(completed spawn after wait) error = %v", err)
	}
	if got, _ := continued.Result["result"].(string); got != "follow-up done" {
		t.Fatalf("continued result = %q, want follow-up done", got)
	}
	if runner.continuePrompt != "next prompt" {
		t.Fatalf("continue prompt = %q, want next prompt", runner.continuePrompt)
	}
}

func TestTaskWriteCanContinueCompletedSpawnChildRepeatedly(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first done"},
		continueResult: delegation.Result{
			State:  delegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "second prompt",
	}); err != nil {
		t.Fatalf("first Write(completed spawn) error = %v", err)
	}
	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "third prompt",
	}); err != nil {
		t.Fatalf("second Write(completed spawn) error = %v", err)
	}
	if runner.continuePrompt != "third prompt" {
		t.Fatalf("last continue prompt = %q, want third prompt", runner.continuePrompt)
	}
}

func TestTaskWriteClearsPreviousSubagentStreamFrames(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:    delegation.Result{State: delegation.StateCompleted},
		waitResult:     delegation.Result{State: delegation.StateCompleted},
		continueResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Text:    "first streamed\n",
		State:   string(delegation.StateCompleted),
		Running: false,
	})
	first, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first) error = %v", err)
	}
	if len(first.Frames) != 1 || first.Frames[0].Text != "first streamed\n" {
		t.Fatalf("first frames = %#v, want first streamed frame", first.Frames)
	}

	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "next prompt",
	}); err != nil {
		t.Fatalf("Write(completed spawn) error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Text:    "second streamed",
		State:   string(delegation.StateRunning),
		Running: true,
	})
	second, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(second) error = %v", err)
	}
	if len(second.Frames) != 1 || second.Frames[0].Text != "second streamed" {
		t.Fatalf("second frames = %#v, want only follow-up output", second.Frames)
	}
	if strings.Contains(second.Frames[0].Text, "first streamed") {
		t.Fatalf("second read replayed previous turn output: %#v", second.Frames)
	}
}

func TestTaskWriteRejectsRunningSpawnChildWithWaitHint(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, OutputPreview: "still running", Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}

	_, err = runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "too soon",
	})
	if err == nil {
		t.Fatal("Write(running spawn) error = nil, want wait hint")
	}
	if !strings.Contains(err.Error(), "TASK wait") {
		t.Fatalf("Write(running spawn) error = %v, want TASK wait hint", err)
	}
	if runner.continuePrompt != "" {
		t.Fatalf("Continue was called for running task with prompt %q", runner.continuePrompt)
	}
}

func TestTerminalServiceReadsRunningSubagentStreamByTaskID(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, OutputPreview: "starting", Running: true},
		waitResult:  delegation.Result{State: delegation.StateRunning, OutputPreview: "starting", Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if started.Ref.TerminalID == "" {
		t.Fatalf("subagent terminal id is empty")
	}
	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent terminal) error = %v", err)
	}
	if !snap.Running {
		t.Fatalf("subagent terminal running = false, want true")
	}
	if len(snap.Frames) != 1 || !strings.Contains(snap.Frames[0].Text, "starting") {
		t.Fatalf("subagent terminal frames = %#v, want starting preview", snap.Frames)
	}
}

func TestSubagentStreamsAppendsIncrementalTerminalFrames(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Text:    "line one\n",
		State:   string(delegation.StateRunning),
		Running: true,
	})
	first, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first subagent frame) error = %v", err)
	}
	if len(first.Frames) != 1 || first.Frames[0].Text != "line one\n" {
		t.Fatalf("first frames = %#v, want line one", first.Frames)
	}

	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Text:    "line two\n",
		State:   string(delegation.StateRunning),
		Running: true,
	})
	second, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref:    stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
		Cursor: first.Cursor,
	})
	if err != nil {
		t.Fatalf("Read(second subagent frame) error = %v", err)
	}
	if len(second.Frames) != 1 || second.Frames[0].Text != "line two\n" {
		t.Fatalf("second frames = %#v, want line two", second.Frames)
	}
}

func TestSubagentStreamsExposeStructuredEventFramesWithoutPreviewFallback(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateRunning, Running: true, OutputPreview: "Searching the Web"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "weather",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(delegation.StateRunning),
		Event: &session.Event{
			Type: session.EventTypeToolCall,
			Protocol: &session.EventProtocol{ToolCall: &session.ProtocolToolCall{
				ID:     "ws-1",
				Name:   "Searching the Web",
				Kind:   "fetch",
				Title:  "Searching the Web",
				Status: "running",
			}},
		},
	})

	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent structured stream) error = %v", err)
	}
	if len(snap.Frames) != 1 {
		t.Fatalf("subagent structured frames = %#v, want one event frame", snap.Frames)
	}
	frame := snap.Frames[0]
	if frame.Text != "" {
		t.Fatalf("structured event frame text = %q, want no preview fallback text", frame.Text)
	}
	if frame.Event == nil || frame.Event.Protocol == nil || frame.Event.Protocol.ToolCall == nil {
		t.Fatalf("structured event frame = %#v, want tool call event", frame)
	}
	if frame.Event.Protocol.ToolCall.Kind != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", frame.Event.Protocol.ToolCall.Kind)
	}
}

func TestSubagentStructuredToolFramesStillSurfaceFinalResult(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateCompleted, Result: "final answer"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "weather",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(delegation.StateRunning),
		Event: &session.Event{
			Type: session.EventTypeToolCall,
			Protocol: &session.EventProtocol{ToolCall: &session.ProtocolToolCall{
				ID:     "ws-1",
				Name:   "Searching the Web",
				Kind:   "fetch",
				Title:  "Searching the Web",
				Status: "running",
			}},
		},
	})

	first, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first structured frame) error = %v", err)
	}
	if len(first.Frames) != 2 || first.Frames[0].Event == nil || strings.TrimSpace(first.Frames[1].Text) != "final answer" {
		t.Fatalf("first frames = %#v, want tool frame followed by final answer", first.Frames)
	}
	if first.Frames[0].Text != "" {
		t.Fatalf("first frame text = %q, want no final result mixed into tool frame", first.Frames[0].Text)
	}
}

func TestSubagentStreamSubscribeClosedFrameCarriesFinalResult(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateCompleted, Result: "### Done\n- `child.txt` written"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "write child",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	var closed *stream.Frame
	for frame, seqErr := range runtime.Streams().Subscribe(ctx, stream.SubscribeRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	}) {
		if seqErr != nil {
			t.Fatalf("Subscribe() error = %v", seqErr)
		}
		if frame != nil && frame.Closed {
			copy := stream.CloneFrame(*frame)
			closed = &copy
			break
		}
	}
	if closed == nil {
		t.Fatal("Subscribe() did not emit closed frame")
	}
	if closed.State != string(task.StateCompleted) {
		t.Fatalf("closed state = %q, want completed", closed.State)
	}
	if got := closed.Text; got != "### Done\n- `child.txt` written" {
		t.Fatalf("closed text = %#v, want final subagent result", got)
	}
}

func TestStartSubagentKeepsEarlyStreamPublishedBeforeTaskRegistration(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:     delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:      delegation.Result{State: delegation.StateRunning, Running: true},
		publishOnSpawn:  true,
		spawnStreamText: "early child output\n",
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent terminal) error = %v", err)
	}
	if len(snap.Frames) != 1 || snap.Frames[0].Text != "early child output\n" {
		t.Fatalf("subagent frames = %#v, want early child output", snap.Frames)
	}
}

func TestSubagentStreamReadInterruptsStaleRunningChild(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true, OutputPreview: "starting"},
		waitErr:     errors.New("impl/agent/acp/subagent: child session \"child-1\" not found"),
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if !started.Running {
		t.Fatalf("started.Running = false, want true")
	}

	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(stale subagent) error = %v", err)
	}
	if snap.Running {
		t.Fatalf("stream snapshot Running = true, want false")
	}
	if snap.State != string(task.StateInterrupted) {
		t.Fatalf("stream snapshot State = %q, want interrupted", snap.State)
	}
	if snap.ExitCode != nil {
		t.Fatalf("stream snapshot ExitCode = %#v, want nil for interrupted subagent", snap.ExitCode)
	}
	if got := snap.FinalText; !strings.Contains(got, "child session") {
		t.Fatalf("stream snapshot final text = %q, want child session detail", got)
	}

	waited, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{TaskID: started.Ref.TaskID})
	if err != nil {
		t.Fatalf("Wait(interrupted subagent) error = %v", err)
	}
	if waited.Running || waited.State != task.StateInterrupted {
		t.Fatalf("Wait() = running %v state %q, want interrupted", waited.Running, waited.State)
	}
}

func TestUpdateACPAgentsPreservesRunnerAndControllerInstances(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{},
		Assembly: assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{
			Name:    "helper",
			Command: "helper-acp",
		}}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	oldSubagents := runtime.subagents
	oldControllers := runtime.controllers

	if err := runtime.UpdateACPAgents([]assembly.AgentConfig{
		{Name: "helper", Command: "helper-acp"},
		{Name: "copilot", Command: "copilot", Args: []string{"--acp"}},
	}); err != nil {
		t.Fatalf("UpdateACPAgents() error = %v", err)
	}
	if runtime.subagents != oldSubagents {
		t.Fatal("UpdateACPAgents replaced subagent runner; existing child runs would be lost")
	}
	if runtime.controllers != oldControllers {
		t.Fatal("UpdateACPAgents replaced controller manager")
	}
	if !localAgentConfigSetHas(runtime.assembly.Agents, "copilot") {
		t.Fatalf("runtime assembly agents = %#v, want copilot", runtime.assembly.Agents)
	}
}

func TestRuntimeSpawnToolRejectsYieldTimeMS(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	targetTool := runtimeSpawnTool{
		base:       spawn.New([]delegation.Agent{{Name: "self"}}),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
		runner:     runner,
	}
	raw, err := json.Marshal(map[string]any{
		"prompt":        "long child task",
		"yield_time_ms": 15000,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	_, err = targetTool.Call(ctx, tool.Call{ID: "spawn-1", Name: spawn.ToolName, Input: raw})
	if err == nil {
		t.Fatal("SPAWN Call() error = nil, want yield_time_ms rejection")
	}
	if !strings.Contains(err.Error(), "yield_time_ms") {
		t.Fatalf("SPAWN Call() error = %v, want yield_time_ms mention", err)
	}
}

func newSubagentTaskTestRuntime(t *testing.T, runner subagent.Runner) (*Runtime, session.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "task-test",
		Workspace: session.WorkspaceRef{
			Key: "task-ws",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{},
		Subagents:    runner,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runtime, activeSession
}

func localAgentConfigSetHas(agents []assembly.AgentConfig, name string) bool {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

type recordingSubagentRunner struct {
	spawnResult     delegation.Result
	waitResult      delegation.Result
	continueResult  delegation.Result
	spawnRequest    delegation.Request
	continueAnchor  delegation.Anchor
	continuePrompt  string
	waitErr         error
	publishOnSpawn  bool
	spawnStreamText string
}

func (r *recordingSubagentRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.spawnRequest = delegation.CloneRequest(req)
	if r.publishOnSpawn && spawn.Streams != nil {
		spawn.Streams.PublishStream(stream.Frame{
			Ref:     stream.Ref{TaskID: strings.TrimSpace(spawn.TaskID)},
			Text:    r.spawnStreamText,
			State:   string(delegation.StateRunning),
			Running: true,
		})
	}
	return delegation.Anchor{SessionID: "child-1", Agent: "helper", AgentID: "helper-1"}, delegation.CloneResult(r.spawnResult), nil
}

func (r *recordingSubagentRunner) Continue(_ context.Context, anchor delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	r.continueAnchor = delegation.CloneAnchor(anchor)
	r.continuePrompt = strings.TrimSpace(req.Prompt)
	return delegation.CloneResult(r.continueResult), nil
}

func (r *recordingSubagentRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	if r.waitErr != nil {
		return delegation.Result{}, r.waitErr
	}
	return delegation.CloneResult(r.waitResult), nil
}

func (r *recordingSubagentRunner) Cancel(context.Context, delegation.Anchor) error {
	return nil
}
