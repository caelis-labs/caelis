package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

func ptrModelMessage(message model.Message) *model.Message {
	return &message
}

func TestSlashSideSubagentReceivesSharedContextAndPublishesPublicDialogue(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "review result"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
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
	entry, err := taskStore.Get(ctx, snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	for _, key := range []string{"result", "final_message", "output", "text", "latest_output", "output_preview"} {
		if _, exists := entry.Result[key]; exists {
			t.Fatalf("side task index unexpectedly contains %q: %#v", key, entry.Result)
		}
	}
	updated, err := runtime.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if len(updated.Participants) != 1 || updated.Participants[0].Role != session.ParticipantRoleSidecar || updated.Participants[0].ContextSyncSeq == 0 {
		t.Fatalf("participants = %#v, want sidecar subagent with context checkpoint", updated.Participants)
	}
}

func TestSlashSideSubagentDoesNotPersistPreviewAsFinalDialogue(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, OutputPreview: "I will inspect files"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	snapshot, err := runtime.StartSubagent(ctx, activeSession.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if snapshot.State != task.StateCompleted {
		t.Fatalf("snapshot state = %q, want completed", snapshot.State)
	}

	loaded, err := runtime.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || event.Scope.Participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			t.Fatalf("side assistant event = %#v, want no durable final from preview-only output", event)
		}
	}
}

func TestSlashSideSubagentPersistsStreamBackedFinalDialogue(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:      delegation.Result{State: delegation.StateCompleted},
		publishOnSpawn:   true,
		spawnStreamText:  "streamed final answer\n",
		spawnStreamState: string(delegation.StateCompleted),
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	snapshot, err := runtime.StartSubagent(ctx, activeSession.SessionRef, "helper", "review", "slash_helper")
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if snapshot.State != task.StateCompleted {
		t.Fatalf("snapshot state = %q, want completed", snapshot.State)
	}

	loaded, err := runtime.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sideAssistant *session.Event
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || event.Scope.Participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			sideAssistant = event
		}
	}
	if sideAssistant == nil || strings.TrimSpace(sideAssistant.Text) != "streamed final answer" {
		t.Fatalf("side assistant event = %#v, want stream-backed final", sideAssistant)
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
	if got := allocateSubagentHandle(activeSession, "codex"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSubagentHandle() = %q, want shared human-name pool handle", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "Anthropic/Claude Agent"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSubagentHandle() = %q, want shared human-name pool handle", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "!!!"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSubagentHandle() = %q, want shared human-name pool handle", got)
	}
	if got := allocateSubagentHandle(session.Session{}, "self"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSubagentHandle(self) = %q, want shared human-name pool handle", got)
	}
	usedSelfHandle := session.Session{Participants: []session.ParticipantBinding{{Label: "@jeff"}}}
	if got := allocateSubagentHandle(usedSelfHandle, "self"); got == "jeff" || !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSubagentHandle(self with used handle) = %q, want unused shared pool handle", got)
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
	firstHandle := taskStringValue(first.Result["handle"])
	secondHandle := taskStringValue(second.Result["handle"])
	if firstHandle == "" || !agenthandle.ContainsPoolName(firstHandle) {
		t.Fatalf("first handle = %q, want shared pool handle", firstHandle)
	}
	if secondHandle == "" || !agenthandle.ContainsPoolName(secondHandle) {
		t.Fatalf("second handle = %q, want shared pool handle", secondHandle)
	}
	if firstHandle == secondHandle {
		t.Fatalf("handles = %q and %q, want unique runtime reservations", firstHandle, secondHandle)
	}
}

func TestTaskRuntimeSyncCanonicalToolResultPersistsSubagentResult(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "raw full child answer\n"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	runtime.tasks.store = newFileTaskStoreForTest(t)

	snapshot, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:      "helper",
		Prompt:     "review",
		Source:     "agent_spawn",
		ParentTool: "SPAWN",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	handle := taskStringValue(snapshot.Result["handle"])
	if handle == "" {
		t.Fatalf("snapshot handle empty: %#v", snapshot.Result)
	}
	entry, err := runtime.tasks.store.Get(ctx, snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get(before sync) error = %v", err)
	}
	if _, exists := entry.Result["result"]; exists {
		t.Fatalf("stored pre-canonical delegated result unexpectedly contains raw output: %#v", entry.Result)
	}

	canonicalText := "canonical truncated child answer\n"
	err = runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			Name:   "SPAWN",
			Status: "completed",
			Output: map[string]any{
				"task_id":       handle,
				"handle":        handle,
				"state":         string(task.StateCompleted),
				"agent":         "helper",
				"final_message": canonicalText,
			},
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult() error = %v", err)
	}
	entry, err = runtime.tasks.store.Get(ctx, snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get(after sync) error = %v", err)
	}
	if got, _ := entry.Result["final_message"].(string); got != canonicalText {
		t.Fatalf("stored final_message = %q, want canonical result", got)
	}
	if _, exists := entry.Result["result"]; exists {
		t.Fatalf("stored result unexpectedly kept pre-canonical field: %#v", entry.Result)
	}
}

func TestLookupStoredSubagentByHandleUsesStoreHandleLookup(t *testing.T) {
	ctx := context.Background()
	runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
	store := &handleLookupTaskStore{
		entry: &task.Entry{
			TaskID:  "task-indexed",
			Kind:    task.KindSubagent,
			Session: activeSession.SessionRef,
			State:   task.StateCompleted,
			Spec:    map[string]any{"handle": "maya"},
			Result:  map[string]any{"state": "completed"},
		},
	}
	runtime.tasks.store = store

	entry, err := runtime.tasks.lookupStoredSubagentByHandle(ctx, activeSession.SessionRef, "@maya")
	if err != nil {
		t.Fatalf("lookupStoredSubagentByHandle() error = %v", err)
	}
	if entry.TaskID != "task-indexed" {
		t.Fatalf("lookupStoredSubagentByHandle() task = %q, want task-indexed", entry.TaskID)
	}
	if !store.handleLookupCalled {
		t.Fatal("store handle lookup was not used")
	}
	if store.listCalled {
		t.Fatal("ListSession was used for handle lookup")
	}
}

type handleLookupTaskStore struct {
	entry              *task.Entry
	handleLookupCalled bool
	listCalled         bool
}

func (s *handleLookupTaskStore) Upsert(context.Context, *task.Entry) error {
	return nil
}

func (s *handleLookupTaskStore) Get(context.Context, string) (*task.Entry, error) {
	return nil, errors.New("not found")
}

func (s *handleLookupTaskStore) ListSession(context.Context, session.SessionRef) ([]*task.Entry, error) {
	s.listCalled = true
	return nil, errors.New("ListSession should not be used for handle lookup")
}

func (s *handleLookupTaskStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, kind task.Kind, handle string) (*task.Entry, error) {
	s.handleLookupCalled = true
	if kind != task.KindSubagent || task.NormalizeHandle(handle) != "maya" {
		return nil, errors.New("not found")
	}
	return task.CloneEntry(s.entry), nil
}

func TestTaskToolResultEventMetaMarksSubagentWriteTarget(t *testing.T) {
	t.Parallel()

	meta := taskToolResultEventMeta(nil, "write", "请追加两行", 0, false, false, false, 0, task.Snapshot{
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
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
				ToolCallID:    "ws-1",
				Kind:          "fetch",
				Title:         "Searching the Web",
				Status:        "running",
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
	update := session.ProtocolUpdateOf(frame.Event)
	if frame.Event == nil || update == nil {
		t.Fatalf("structured event frame = %#v, want tool call event", frame)
	}
	if update.Kind != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", update.Kind)
	}
}

func TestSubagentStreamsExposeSemanticAssistantEventText(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "list files",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(delegation.StateRunning),
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Message: ptrModelMessage(model.NewTextMessage(model.RoleAssistant, "child output\n")),
		},
	})

	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent semantic stream) error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != "child output\n" {
		t.Fatalf("semantic subagent frame text = %q, want child output", got)
	}
	if len(snap.Frames) != 1 || snap.Frames[0].Event == nil {
		t.Fatalf("semantic subagent frames = %#v, want one frame preserving event", snap.Frames)
	}
}

func TestSubagentStreamsDoNotExposeSemanticReasoningAsParentOutput(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "think",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runtime.tasks.PublishStream(stream.Frame{
		Ref:     stream.Ref{TaskID: started.Ref.TaskID},
		Running: true,
		State:   string(delegation.StateRunning),
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Message: ptrModelMessage(model.NewReasoningMessage(model.RoleAssistant, "private thought", model.ReasoningVisibilityVisible)),
		},
	})

	snap, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Read(subagent semantic reasoning stream) error = %v", err)
	}
	if len(snap.Frames) != 1 || snap.Frames[0].Event == nil {
		t.Fatalf("semantic reasoning frames = %#v, want one structured event frame", snap.Frames)
	}
	if got := streamFrameText(snap.Frames); got != "" {
		t.Fatalf("semantic reasoning parent output = %q, want empty", got)
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
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
				ToolCallID:    "ws-1",
				Kind:          "fetch",
				Title:         "Searching the Web",
				Status:        "running",
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
		waitErr:     errors.New("test subagent runner: child session \"child-1\" not found"),
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

func TestRuntimeSpawnToolRejectsUnknownArgsBeforeRequiredPrompt(t *testing.T) {
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
		"yield_time_ms": 15000,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	_, err = targetTool.Call(ctx, tool.Call{ID: "spawn-1", Name: spawn.ToolName, Input: raw})
	if err == nil {
		t.Fatal("SPAWN Call() error = nil, want yield_time_ms rejection")
	}
	if strings.Contains(err.Error(), "prompt") || !strings.Contains(err.Error(), "yield_time_ms") {
		t.Fatalf("SPAWN Call() error = %v, want unknown arg rejection before prompt requirement", err)
	}
}

func TestRuntimeSpawnToolAllowsSelfDefaultAndRejectsRawACPWhenEnumExists(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	targetTool := runtimeSpawnTool{
		base:       spawn.New([]delegation.Agent{{Name: "self"}, {Name: "reviewer"}}),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
		runner:     runner,
	}
	for _, input := range []map[string]any{
		{"prompt": "review this"},
		{"agent": "self", "prompt": "review this"},
	} {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-1", Name: spawn.ToolName, Input: raw}); err != nil {
			t.Fatalf("SPAWN Call(%v) error = %v", input, err)
		}
		if runner.spawnRequest.Agent != "self" {
			t.Fatalf("spawn agent = %q, want self", runner.spawnRequest.Agent)
		}
	}

	raw, err := json.Marshal(map[string]any{"agent": "codex", "prompt": "review this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-reject", Name: spawn.ToolName, Input: raw}); err == nil {
		t.Fatal("SPAWN Call(codex) error = nil, want rejection")
	}

	raw, err = json.Marshal(map[string]any{"agent": "reviewer", "prompt": "review this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-2", Name: spawn.ToolName, Input: raw}); err != nil {
		t.Fatalf("SPAWN Call(reviewer) error = %v", err)
	}
	if runner.spawnRequest.Agent != "reviewer" {
		t.Fatalf("spawn agent = %q, want reviewer", runner.spawnRequest.Agent)
	}
}

func TestRuntimeSpawnToolKeepsImplicitSelfFallback(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	targetTool := runtimeSpawnTool{
		base:       spawn.New([]delegation.Agent{{Name: "self"}}),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
		runner:     runner,
	}
	raw, err := json.Marshal(map[string]any{"prompt": "inspect this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-1", Name: spawn.ToolName, Input: raw}); err != nil {
		t.Fatalf("SPAWN Call(implicit self) error = %v", err)
	}
	if runner.spawnRequest.Agent != "self" {
		t.Fatalf("spawn agent = %q, want self", runner.spawnRequest.Agent)
	}
	raw, err = json.Marshal(map[string]any{"agent": "codex", "prompt": "inspect this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-2", Name: spawn.ToolName, Input: raw}); err == nil {
		t.Fatal("SPAWN Call(codex) error = nil, want rejection")
	}
}

func TestRuntimeSpawnToolPassesApprovalModeToChild(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	targetTool := runtimeSpawnTool{
		base:         spawn.New([]delegation.Agent{{Name: "self"}}),
		session:      activeSession,
		sessionRef:   activeSession.SessionRef,
		tasks:        runtime.tasks,
		runner:       runner,
		approvalMode: "manual",
	}
	raw, err := json.Marshal(map[string]any{"prompt": "inspect this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-1", Name: spawn.ToolName, Input: raw}); err != nil {
		t.Fatalf("SPAWN Call() error = %v", err)
	}
	if got := runner.spawnContext.ApprovalMode; got != "manual" {
		t.Fatalf("spawn approval mode = %q, want manual", got)
	}
}

func TestStartSubagentWithOptionsInheritsSessionApprovalMode(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	if err := runtime.sessions.UpdateState(ctx, activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[approval.StateCurrentApprovalMode] = "manual"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if _, err := runtime.StartSubagentWithOptions(ctx, activeSession.SessionRef, "self", "inspect this", "slash", StartSubagentOptions{}); err != nil {
		t.Fatalf("StartSubagentWithOptions() error = %v", err)
	}
	if got := runner.spawnContext.ApprovalMode; got != "manual" {
		t.Fatalf("spawn approval mode = %q, want manual", got)
	}
}

func TestRuntimeCurrentApprovalModeUsesConfiguredDefault(t *testing.T) {
	runtime := &Runtime{defaultApprovalMode: approval.ModeManual}
	if got := runtime.currentApprovalMode(nil); got != approval.ModeManual {
		t.Fatalf("currentApprovalMode(empty) = %q, want manual", got)
	}
	state := map[string]any{approval.StateCurrentApprovalMode: string(approval.ModeAutoReview)}
	if got := runtime.currentApprovalMode(state); got != approval.ModeAutoReview {
		t.Fatalf("currentApprovalMode(override) = %q, want auto-review", got)
	}
}

func TestStartSubagentWithOptionsUsesRuntimeDefaultApprovalMode(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	runtime.defaultApprovalMode = approval.ModeManual

	if _, err := runtime.StartSubagentWithOptions(ctx, activeSession.SessionRef, "self", "inspect this", "slash", StartSubagentOptions{}); err != nil {
		t.Fatalf("StartSubagentWithOptions() error = %v", err)
	}
	if got := runner.spawnContext.ApprovalMode; got != "manual" {
		t.Fatalf("spawn approval mode = %q, want manual", got)
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

type recordingSubagentRunner struct {
	spawnResult        delegation.Result
	waitResult         delegation.Result
	continueResult     delegation.Result
	spawnRequest       delegation.Request
	continueAnchor     delegation.Anchor
	continuePrompt     string
	waitErr            error
	publishOnSpawn     bool
	spawnStreamText    string
	spawnStreamState   string
	spawnStreamRunning bool
	spawnContext       subagent.SpawnContext
}

func (r *recordingSubagentRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.spawnRequest = delegation.CloneRequest(req)
	r.spawnContext = spawn
	if r.publishOnSpawn && spawn.Streams != nil {
		state := strings.TrimSpace(r.spawnStreamState)
		running := r.spawnStreamRunning
		if state == "" {
			state = string(delegation.StateRunning)
			running = true
		}
		spawn.Streams.PublishStream(stream.Frame{
			Ref:     stream.Ref{TaskID: strings.TrimSpace(spawn.TaskID)},
			Text:    r.spawnStreamText,
			State:   state,
			Running: running,
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
