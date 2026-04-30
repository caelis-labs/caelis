package local

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	spawntool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/spawn"
)

func TestSlashSideSubagentReceivesSharedContextAndPublishesPublicDialogue(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "review result"},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)
	userMessage := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "previous request")
	assistantMessage := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "previous answer")
	for _, event := range []*sdksession.Event{{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &userMessage,
		Text:       "previous request",
	}, {
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &assistantMessage,
		Text:       "previous answer",
	}} {
		if _, err := runtime.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{SessionRef: session.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	snapshot, err := runtime.StartSubagent(ctx, session.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if snapshot.State != sdktask.StateCompleted {
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

	loaded, err := runtime.sessions.LoadSession(ctx, sdksession.LoadSessionRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sideUser, sideAssistant *sdksession.Event
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || event.Scope.Participant.Role != sdksession.ParticipantRoleSidecar {
			continue
		}
		switch sdksession.EventTypeOf(event) {
		case sdksession.EventTypeUser:
			sideUser = event
		case sdksession.EventTypeAssistant:
			sideAssistant = event
		}
	}
	if sideUser == nil || strings.TrimSpace(sideUser.Text) != "review" || !sdksession.IsMainInvocationVisibleEvent(sideUser) {
		t.Fatalf("side user event = %#v, want public review request", sideUser)
	}
	if sideAssistant == nil || strings.TrimSpace(sideAssistant.Text) != "review result" || !sdksession.IsMainInvocationVisibleEvent(sideAssistant) {
		t.Fatalf("side assistant event = %#v, want public final result", sideAssistant)
	}
	updated, err := runtime.sessions.Session(ctx, session.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if len(updated.Participants) != 1 || updated.Participants[0].Role != sdksession.ParticipantRoleSidecar || updated.Participants[0].ContextSyncSeq == 0 {
		t.Fatalf("participants = %#v, want sidecar subagent with context checkpoint", updated.Participants)
	}
}

func TestSubagentTaskIDForHandleAllowsSidecarCustomSource(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		Participants: []sdksession.ParticipantBinding{{
			ID:           "side-1",
			Kind:         sdksession.ParticipantKindSubagent,
			Role:         sdksession.ParticipantRoleSidecar,
			Label:        "@jeff",
			Source:       "custom_codex",
			DelegationID: "task-side",
		}, {
			ID:           "delegated-1",
			Kind:         sdksession.ParticipantKindSubagent,
			Role:         sdksession.ParticipantRoleDelegated,
			Label:        "@jude",
			Source:       "agent_spawn",
			DelegationID: "task-delegated",
		}},
	}
	taskID, binding, ok := subagentTaskIDForHandle(session, "jeff")
	if !ok || taskID != "task-side" || binding.ID != "side-1" {
		t.Fatalf("subagentTaskIDForHandle(sidecar) = (%q, %#v, %v), want custom-source sidecar", taskID, binding, ok)
	}
	if _, _, ok := subagentTaskIDForHandle(session, "jude"); ok {
		t.Fatal("subagentTaskIDForHandle(delegated) = ok, want hidden from @handle")
	}
}

func TestTaskWriteContinuesCompletedSpawnChild(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "first done"},
		continueResult: sdkdelegation.Result{
			State:  sdkdelegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if started.State != sdktask.StateCompleted {
		t.Fatalf("started state = %q, want completed", started.State)
	}

	continued, err := runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
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
		spawnResult:    sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "side done"},
		continueResult: sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "continued"},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	side, err := runtime.StartSubagent(ctx, session.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent(side) error = %v", err)
	}
	if _, err := runtime.tasks.Wait(ctx, session.SessionRef, sdktask.ControlRequest{
		TaskID: taskStringValue(side.Result["handle"]),
		Source: "agent_tool",
	}); err == nil || !strings.Contains(err.Error(), "cannot control user-created side subagent") {
		t.Fatalf("TASK wait on side err = %v, want isolation error", err)
	}
	if _, err := runtime.ContinueSubagentByHandle(ctx, session.SessionRef, taskStringValue(side.Result["handle"]), "follow up", 0); err != nil {
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

	updatedSession, err := runtime.sessions.Session(ctx, session.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	delegated, err := runtime.tasks.StartSubagent(ctx, updatedSession, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "internal task",
	})
	if err != nil {
		t.Fatalf("StartSubagent(delegated) error = %v", err)
	}
	if _, err := runtime.ContinueSubagentByHandle(ctx, session.SessionRef, taskStringValue(delegated.Result["handle"]), "user follow up", 0); err == nil {
		t.Fatal("ContinueSubagentByHandle(delegated) succeeded, want not found")
	}
}

func TestTaskWriteContinuesSpawnChildAfterWaitCompletes(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, OutputPreview: "working", Running: true},
		waitResult:  sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "first done"},
		continueResult: sdkdelegation.Result{
			State:  sdkdelegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	completed, err := runtime.tasks.Wait(ctx, session.SessionRef, sdktask.ControlRequest{
		TaskID: started.Ref.TaskID,
	})
	if err != nil {
		t.Fatalf("Wait(spawn) error = %v", err)
	}
	if completed.State != sdktask.StateCompleted {
		t.Fatalf("completed state = %q, want completed", completed.State)
	}

	continued, err := runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "first done"},
		continueResult: sdkdelegation.Result{
			State:  sdkdelegation.StateCompleted,
			Result: "follow-up done",
		},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if _, err := runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "second prompt",
	}); err != nil {
		t.Fatalf("first Write(completed spawn) error = %v", err)
	}
	if _, err := runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
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
		spawnResult:    sdkdelegation.Result{State: sdkdelegation.StateCompleted},
		waitResult:     sdkdelegation.Result{State: sdkdelegation.StateCompleted},
		continueResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Stream:  "stdout",
		Text:    "first streamed\n",
		State:   string(sdkdelegation.StateCompleted),
		Running: false,
	})
	first, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first) error = %v", err)
	}
	if len(first.Frames) != 1 || first.Frames[0].Text != "first streamed\n" {
		t.Fatalf("first frames = %#v, want first streamed frame", first.Frames)
	}

	if _, err := runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
		TaskID: started.Ref.TaskID,
		Input:  "next prompt",
	}); err != nil {
		t.Fatalf("Write(completed spawn) error = %v", err)
	}
	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Stream:  "stdout",
		Text:    "second streamed",
		State:   string(sdkdelegation.StateRunning),
		Running: true,
	})
	second, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, OutputPreview: "still running", Running: true},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}

	_, err = runtime.tasks.Write(ctx, session.SessionRef, sdktask.ControlRequest{
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, OutputPreview: "starting", Running: true},
		waitResult:  sdkdelegation.Result{State: sdkdelegation.StateRunning, OutputPreview: "starting", Running: true},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if started.Ref.TerminalID == "" {
		t.Fatalf("subagent terminal id is empty")
	}
	snap, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
		waitResult:  sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Stream:  "stdout",
		Text:    "line one\n",
		State:   string(sdkdelegation.StateRunning),
		Running: true,
	})
	first, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first subagent frame) error = %v", err)
	}
	if len(first.Frames) != 1 || first.Frames[0].Text != "line one\n" {
		t.Fatalf("first frames = %#v, want line one", first.Frames)
	}

	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Stream:  "stdout",
		Text:    "line two\n",
		State:   string(sdkdelegation.StateRunning),
		Running: true,
	})
	second, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref:    sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
		waitResult:  sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true, OutputPreview: "Searching the Web"},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "weather",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(sdkdelegation.StateRunning),
		Event: &sdksession.Event{
			Type: sdksession.EventTypeToolCall,
			Protocol: &sdksession.EventProtocol{ToolCall: &sdksession.ProtocolToolCall{
				ID:     "ws-1",
				Name:   "Searching the Web",
				Kind:   "fetch",
				Title:  "Searching the Web",
				Status: "running",
			}},
		},
	})

	snap, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
		waitResult:  sdkdelegation.Result{State: sdkdelegation.StateCompleted, Result: "final answer"},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "weather",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(sdkstream.Frame{
		Ref:     sdkstream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(sdkdelegation.StateRunning),
		Event: &sdksession.Event{
			Type: sdksession.EventTypeToolCall,
			Protocol: &sdksession.EventProtocol{ToolCall: &sdksession.ProtocolToolCall{
				ID:     "ws-1",
				Name:   "Searching the Web",
				Kind:   "fetch",
				Title:  "Searching the Web",
				Status: "running",
			}},
		},
	})

	first, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(first structured frame) error = %v", err)
	}
	if len(first.Frames) != 2 || first.Frames[0].Event == nil || first.Frames[1].Text != "final answer" {
		t.Fatalf("first frames = %#v, want tool frame followed by final answer", first.Frames)
	}
	if first.Frames[0].Text != "" {
		t.Fatalf("first frame text = %q, want no final result mixed into tool frame", first.Frames[0].Text)
	}
}

func TestStartSubagentKeepsEarlyStreamPublishedBeforeTaskRegistration(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:     sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
		waitResult:      sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
		publishOnSpawn:  true,
		spawnStreamText: "early child output\n",
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, session, session.SessionRef, runner, sdktask.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	snap, err := runtime.Streams().Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{SessionID: session.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent terminal) error = %v", err)
	}
	if len(snap.Frames) != 1 || snap.Frames[0].Text != "early child output\n" {
		t.Fatalf("subagent frames = %#v, want early child output", snap.Frames)
	}
}

func TestUpdateACPAgentsPreservesRunnerAndControllerInstances(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{},
		Assembly: sdkplugin.ResolvedAssembly{Agents: []sdkplugin.AgentConfig{{
			Name:    "helper",
			Command: "helper-acp",
		}}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	oldSubagents := runtime.subagents
	oldControllers := runtime.controllers

	if err := runtime.UpdateACPAgents([]sdkplugin.AgentConfig{
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
		spawnResult: sdkdelegation.Result{State: sdkdelegation.StateRunning, Running: true},
	}
	runtime, session := newSubagentTaskTestRuntime(t, runner)
	tool := runtimeSpawnTool{
		base:       spawntool.New([]sdkdelegation.Agent{{Name: "self"}}),
		session:    session,
		sessionRef: session.SessionRef,
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

	_, err = tool.Call(ctx, sdktool.Call{ID: "spawn-1", Name: spawntool.ToolName, Input: raw})
	if err == nil {
		t.Fatal("SPAWN Call() error = nil, want yield_time_ms rejection")
	}
	if !strings.Contains(err.Error(), "yield_time_ms") {
		t.Fatalf("SPAWN Call() error = %v, want yield_time_ms mention", err)
	}
}

func newSubagentTaskTestRuntime(t *testing.T, runner sdksubagent.Runner) (*Runtime, sdksession.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "task-test",
		Workspace: sdksession.WorkspaceRef{
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
	return runtime, session
}

func localAgentConfigSetHas(agents []sdkplugin.AgentConfig, name string) bool {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

type recordingSubagentRunner struct {
	spawnResult     sdkdelegation.Result
	waitResult      sdkdelegation.Result
	continueResult  sdkdelegation.Result
	spawnRequest    sdkdelegation.Request
	continueAnchor  sdkdelegation.Anchor
	continuePrompt  string
	publishOnSpawn  bool
	spawnStreamText string
}

func (r *recordingSubagentRunner) Spawn(_ context.Context, spawn sdksubagent.SpawnContext, req sdkdelegation.Request) (sdkdelegation.Anchor, sdkdelegation.Result, error) {
	r.spawnRequest = sdkdelegation.CloneRequest(req)
	if r.publishOnSpawn && spawn.Streams != nil {
		spawn.Streams.PublishStream(sdkstream.Frame{
			Ref:     sdkstream.Ref{TaskID: strings.TrimSpace(spawn.TaskID)},
			Stream:  "stdout",
			Text:    r.spawnStreamText,
			State:   string(sdkdelegation.StateRunning),
			Running: true,
		})
	}
	return sdkdelegation.Anchor{SessionID: "child-1", Agent: "helper", AgentID: "helper-1"}, sdkdelegation.CloneResult(r.spawnResult), nil
}

func (r *recordingSubagentRunner) Continue(_ context.Context, anchor sdkdelegation.Anchor, req sdkdelegation.ContinueRequest) (sdkdelegation.Result, error) {
	r.continueAnchor = sdkdelegation.CloneAnchor(anchor)
	r.continuePrompt = strings.TrimSpace(req.Prompt)
	return sdkdelegation.CloneResult(r.continueResult), nil
}

func (r *recordingSubagentRunner) Wait(context.Context, sdkdelegation.Anchor, int) (sdkdelegation.Result, error) {
	return sdkdelegation.CloneResult(r.waitResult), nil
}

func (r *recordingSubagentRunner) Cancel(context.Context, sdkdelegation.Anchor) error {
	return nil
}
