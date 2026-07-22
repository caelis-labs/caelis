package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
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

func mustSealPlacement(t *testing.T, value placement.Placement) placement.Placement {
	t.Helper()
	sealed, err := placement.Seal(value)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
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
	if prompt := runner.spawnRequest.Prompt; !strings.Contains(prompt, `<caelis_background version="1">`) ||
		!strings.Contains(prompt, `"user_messages":["previous request"]`) ||
		!strings.Contains(prompt, `"assistant_summary":"previous answer"`) ||
		!strings.Contains(prompt, "<caelis_current_request>\nreview") {
		t.Fatalf("spawn prompt missing shared side context:\n%s", prompt)
	} else if strings.Count(prompt, "review") != 1 {
		t.Fatalf("spawn prompt duplicated current request:\n%s", prompt)
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

func TestSubagentRoleComesFromNeutralRequestNotProductSource(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"}}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	side, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "side", Source: "custom-origin", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatalf("StartSubagent(sidecar) error = %v", err)
	}
	delegated, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "delegated", Source: "slash_agent",
	})
	if err != nil {
		t.Fatalf("StartSubagent(delegated) error = %v", err)
	}
	if got := session.ParticipantRole(taskStringValue(side.Metadata["participant_role"])); got != session.ParticipantRoleSidecar {
		t.Fatalf("explicit sidecar role = %q, want sidecar", got)
	}
	if got := session.ParticipantRole(taskStringValue(delegated.Metadata["participant_role"])); got != session.ParticipantRoleDelegated {
		t.Fatalf("slash_agent source role = %q, want default delegated", got)
	}
}

func TestSubagentControlAuthorizationUsesNeutralPrincipalNotProductSource(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"}}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	side, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "side", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatalf("StartSubagent(sidecar) error = %v", err)
	}
	if _, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: side.Ref.TaskID, Source: "agent_tool", Principal: session.ActorKindUser,
	}); err != nil {
		t.Fatalf("user principal with product-looking source error = %v", err)
	}
	if _, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: side.Ref.TaskID, Source: "custom-origin", Principal: session.ActorKindTool,
	}); err == nil || !strings.Contains(err.Error(), "tool principal") {
		t.Fatalf("tool principal sidecar error = %v, want isolation error", err)
	}

	delegated, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "delegated", Role: session.ParticipantRoleDelegated,
	})
	if err != nil {
		t.Fatalf("StartSubagent(delegated) error = %v", err)
	}
	_, err = runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: delegated.Ref.TaskID, Input: "follow up", Source: "custom-origin", Principal: session.ActorKindUser,
	})
	if err == nil || !strings.Contains(err.Error(), "user principal") {
		t.Fatalf("user principal delegated error = %v, want isolation error", err)
	}
	if _, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: delegated.Ref.TaskID, Principal: session.ActorKind("unknown"), Source: "agent_tool",
	}); err == nil || !strings.Contains(err.Error(), "unsupported control principal") {
		t.Fatalf("unknown principal error = %v, want fail-closed rejection", err)
	}
	if _, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: delegated.Ref.TaskID, Source: "controller-looking-source",
	}); err == nil || !strings.Contains(err.Error(), "unsupported control principal") {
		t.Fatalf("empty principal error = %v, want fail-closed rejection", err)
	}
}

func TestSubagentRejectsUnknownNeutralRoleBeforeSpawn(t *testing.T) {
	t.Parallel()

	runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted}}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	_, err := runtime.tasks.StartSubagent(context.Background(), activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review", Role: session.ParticipantRole("owner"), Source: "slash_agent",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported subagent participant role") {
		t.Fatalf("StartSubagent(unknown role) error = %v, want fail-closed rejection", err)
	}
	if runner.spawnTargetRequest.Target.Selector != "" {
		t.Fatalf("Spawn() request = %#v, want no external spawn before role validation", runner.spawnRequest)
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
		spawnResult:    delegation.Result{State: delegation.StateCompleted, Result: "done"},
		continueResult: delegation.Result{State: delegation.StateCompleted, Result: "continued"},
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
		Agent:  "helper",
		Prompt: "review",
		Source: "agent_spawn",
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

func TestResolveTaskHandleUsesStoreHandleLookup(t *testing.T) {
	ctx := context.Background()
	runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
	store := &handleLookupTaskStore{
		entry: &task.Entry{
			TaskID:  "task-indexed",
			Handle:  "maya",
			Kind:    task.KindSubagent,
			Session: activeSession.SessionRef,
			State:   task.StateCompleted,
			Result:  map[string]any{"state": "completed"},
		},
	}
	runtime.tasks.store = store

	identity, err := runtime.tasks.resolveTaskHandle(ctx, activeSession.SessionRef, "@maya")
	if err != nil {
		t.Fatalf("resolveTaskHandle() error = %v", err)
	}
	if identity.taskID != "task-indexed" || identity.kind != task.KindSubagent {
		t.Fatalf("resolveTaskHandle() identity = %#v, want task-indexed subagent", identity)
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

func (s *handleLookupTaskStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, handle string) (*task.Entry, error) {
	s.handleLookupCalled = true
	if task.NormalizeHandle(handle) != "maya" {
		return nil, errors.New("not found")
	}
	return task.CloneEntry(s.entry), nil
}

func TestTaskToolResultEventMetaMarksSubagentWriteTarget(t *testing.T) {
	t.Parallel()

	meta := taskToolResultEventMeta(nil, "write", "请追加两行", 0, task.Snapshot{
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
	if targetTool["name"] != "Task" || targetTool["action"] != "write" || targetTool["target_kind"] != "subagent" || targetTool["target_handle"] != "maya" || targetTool["input"] != "请追加两行" {
		t.Fatalf("runtime.tool = %#v, want Task write subagent target", targetTool)
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
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "next prompt",
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
		TaskID:    side.Ref.TaskID,
		Principal: session.ActorKindTool,
		Source:    "agent_tool",
	}); err == nil || !strings.Contains(err.Error(), "tool principal") {
		t.Fatalf("TASK wait on side err = %v, want isolation error", err)
	}
	if _, err := runtime.ContinueSubagentByHandle(ctx, activeSession.SessionRef, taskStringValue(side.Result["handle"]), "follow up", 0); err != nil {
		t.Fatalf("ContinueSubagentByHandle(side) error = %v", err)
	}
	if runner.continuePrompt != "follow up" {
		t.Fatalf("side continuation prompt = %q, want raw current request for an empty offset", runner.continuePrompt)
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
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
	})
	if err != nil {
		t.Fatalf("Wait(spawn) error = %v", err)
	}
	if completed.State != task.StateCompleted {
		t.Fatalf("completed state = %q, want completed", completed.State)
	}

	continued, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "next prompt",
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
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "second prompt",
	}); err != nil {
		t.Fatalf("first Write(completed spawn) error = %v", err)
	}
	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "third prompt",
	}); err != nil {
		t.Fatalf("second Write(completed spawn) error = %v", err)
	}
	if runner.continuePrompt != "third prompt" {
		t.Fatalf("last continue prompt = %q, want third prompt", runner.continuePrompt)
	}
}

func TestTaskWriteContinuesSubagentStreamCursorAcrossTurns(t *testing.T) {
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
	if len(first.Frames) != 2 || first.Frames[0].Text != "first streamed\n" || !first.Frames[1].Closed {
		t.Fatalf("first frames = %#v, want first streamed frame and terminal frame", first.Frames)
	}

	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "next prompt",
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
		Ref:    stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
		Cursor: first.Cursor,
	})
	if err != nil {
		t.Fatalf("Read(second) error = %v", err)
	}
	if len(second.Frames) != 2 || second.Frames[0].Text != "second streamed" || !second.Frames[1].Closed {
		t.Fatalf("second frames = %#v, want follow-up output and its terminal frame", second.Frames)
	}
	if strings.Contains(second.Frames[0].Text, "first streamed") {
		t.Fatalf("second read replayed previous turn output: %#v", second.Frames)
	}
	if second.Cursor.Events <= first.Cursor.Events {
		t.Fatalf("continued event cursor = %d, want greater than first turn cursor %d", second.Cursor.Events, first.Cursor.Events)
	}
	if second.Cursor.Output <= first.Cursor.Output {
		t.Fatalf("continued output cursor = %d, want greater than first turn cursor %d", second.Cursor.Output, first.Cursor.Output)
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
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool,
		Input: "too soon",
	})
	if err == nil {
		t.Fatal("Write(running spawn) error = nil, want wait hint")
	}
	if !strings.Contains(err.Error(), "Task wait") {
		t.Fatalf("Write(running spawn) error = %v, want Task wait hint", err)
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

func TestSubagentTaskToolMetaCarriesPhysicalTurnCursorAndSpawnParent(t *testing.T) {
	meta := taskToolMeta(task.Snapshot{
		Kind:        task.KindSubagent,
		EventCursor: 17,
		Metadata: map[string]any{
			"turn_id":     "task-1:2",
			"parent_call": "spawn-call-1",
			"parent_tool": "SPAWN",
		},
	})
	caelisMeta, ok := meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("caelis metadata = %#v, want object", meta["caelis"])
	}
	runtimeMeta, ok := caelisMeta["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime metadata = %#v, want object", caelisMeta["runtime"])
	}
	taskMeta, ok := runtimeMeta["task"].(map[string]any)
	if !ok {
		t.Fatalf("task metadata = %#v, want object", runtimeMeta["task"])
	}
	if got := taskStringValue(taskMeta["turn_id"]); got != "task-1:2" {
		t.Fatalf("turn_id = %q, want task-1:2", got)
	}
	if got, ok := taskInt64Value(taskMeta["event_cursor"]); !ok || got != 17 {
		t.Fatalf("event_cursor = %#v, want 17", taskMeta["event_cursor"])
	}
	if taskStringValue(taskMeta["parent_call"]) != "spawn-call-1" || taskStringValue(taskMeta["parent_tool"]) != "SPAWN" {
		t.Fatalf("parent task metadata = %#v, want canonical Spawn relation", taskMeta)
	}
}

func TestSubagentTaskToolPayloadCarriesCanonicalFinalAndSpawnParent(t *testing.T) {
	payload := taskToolPayload(task.Snapshot{
		Kind:  task.KindSubagent,
		State: task.StateCompleted,
		Result: map[string]any{
			"final_message": "## 完成\n\n- 保留格式",
		},
		Metadata: map[string]any{
			"parent_call": "spawn-call-1",
			"parent_tool": "SPAWN",
		},
	})

	if got := taskStringValue(payload["final_message"]); got != "## 完成\n\n- 保留格式" {
		t.Fatalf("final_message = %q, want exact canonical Final Message", got)
	}
	if got := taskStringValue(payload["target_kind"]); got != "subagent" {
		t.Fatalf("target_kind = %q, want subagent", got)
	}
	if taskStringValue(payload["parent_call"]) != "spawn-call-1" || taskStringValue(payload["parent_tool"]) != "SPAWN" {
		t.Fatalf("parent relation payload = %#v, want canonical Spawn relation", payload)
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
	if len(first.Frames) != 3 || first.Frames[0].Event == nil || strings.TrimSpace(first.Frames[1].Text) != "final answer" || !first.Frames[2].Closed {
		t.Fatalf("first frames = %#v, want tool frame, final answer, and terminal frame", first.Frames)
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

	waited, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{TaskID: started.Ref.TaskID, Principal: session.ActorKindController})
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

func TestRuntimeSpawnToolIsParallelSafeAndConcurrentAttachmentsConverge(t *testing.T) {
	t.Parallel()

	runner := newOverlappingSubagentRunner(3)
	runner.autoRelease = true
	defer func() {
		select {
		case <-runner.release:
		default:
			close(runner.release)
		}
	}()
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	wrapped := runtimeSpawnTool{
		base: spawn.New([]delegation.Agent{{Name: "self"}}), session: activeSession,
		sessionRef: activeSession.SessionRef, tasks: runtime.tasks, runner: runner,
	}
	if !wrapped.Definition().Capabilities.ParallelSafe {
		t.Fatal("runtime Spawn wrapper is not ParallelSafe")
	}
	stepModel := &threeSpawnStepModel{}
	chatAgent, err := chat.NewWithTools("chat", stepModel, []tool.Tool{wrapped}, "Use Spawn.")
	if err != nil {
		t.Fatal(err)
	}
	user := model.NewTextMessage(model.RoleUser, "inspect three things")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, runErr := range chatAgent.Run(agent.NewContext(agent.ContextSpec{
		Context: ctx, Session: activeSession,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &user, Text: "inspect three things"}},
	})) {
		if runErr != nil {
			t.Fatalf("same-step Spawn run error = %v", runErr)
		}
	}
	if runner.maxActive() < 3 {
		t.Fatalf("external Spawn max concurrency = %d, want 3", runner.maxActive())
	}
	if want := []string{"spawn-1", "spawn-2", "spawn-3"}; !equalStrings(stepModel.resultCallIDs, want) {
		t.Fatalf("tool result call order = %v, want %v", stepModel.resultCallIDs, want)
	}
	visibleTaskIDs := map[string]struct{}{}
	for _, taskID := range stepModel.resultTaskIDs {
		visibleTaskIDs[taskID] = struct{}{}
	}
	if len(visibleTaskIDs) != 3 {
		t.Fatalf("visible Task ids = %v, want three ordered Spawn results", stepModel.resultTaskIDs)
	}
	entries, err := runtime.tasks.store.ListSession(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	taskIDs := map[string]struct{}{}
	for _, entry := range entries {
		taskIDs[entry.TaskID] = struct{}{}
		if entry.Session.SessionID != activeSession.SessionID {
			t.Fatalf("Task %q belongs to Session %q, want %q", entry.TaskID, entry.Session.SessionID, activeSession.SessionID)
		}
	}
	if len(taskIDs) != 3 {
		t.Fatalf("Task ids = %v, want three isolated Tasks", taskIDs)
	}
	loaded, err := runtime.sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Participants) != 3 {
		t.Fatalf("participants = %#v, want all three concurrent attachments", loaded.Participants)
	}
}

type threeSpawnStepModel struct {
	calls         int
	resultCallIDs []string
	resultTaskIDs []string
}

func (*threeSpawnStepModel) Name() string { return "three-spawn-step" }

func (*threeSpawnStepModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}

func (m *threeSpawnStepModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		for _, message := range req.Messages {
			for _, result := range message.ToolResults() {
				m.resultCallIDs = append(m.resultCallIDs, result.ToolUseID)
				for _, part := range result.Content {
					if part.Kind != model.PartKindJSON || part.JSON == nil {
						continue
					}
					var payload map[string]any
					if json.Unmarshal(part.JSONValue(), &payload) == nil {
						if handle, _ := payload["handle"].(string); strings.TrimSpace(handle) != "" {
							m.resultTaskIDs = append(m.resultTaskIDs, strings.TrimSpace(handle))
						}
					}
				}
			}
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{
			TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted,
		}
		if callIndex == 1 {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
				{ID: "spawn-1", Name: spawn.ToolName, Args: `{"agent":"self","prompt":"inspect one"}`},
				{ID: "spawn-2", Name: spawn.ToolName, Args: `{"agent":"self","prompt":"inspect two"}`},
				{ID: "spawn-3", Name: spawn.ToolName, Args: `{"agent":"self","prompt":"inspect three"}`},
			}, "")
			response.FinishReason = model.FinishReasonToolCalls
		} else {
			response.Message = model.NewTextMessage(model.RoleAssistant, "done")
			response.FinishReason = model.FinishReasonStop
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type overlappingSubagentRunner struct {
	want    int
	ready   chan struct{}
	release chan struct{}

	mu          sync.Mutex
	active      int
	maxSeen     int
	spawnedID   int
	autoRelease bool
	releaseOnce sync.Once
}

func newOverlappingSubagentRunner(want int) *overlappingSubagentRunner {
	return &overlappingSubagentRunner{want: want, ready: make(chan struct{}, want), release: make(chan struct{})}
}

func (r *overlappingSubagentRunner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.maxSeen {
		r.maxSeen = r.active
	}
	r.spawnedID++
	id := r.spawnedID
	if r.autoRelease && r.active >= r.want {
		r.releaseOnce.Do(func() { close(r.release) })
	}
	r.mu.Unlock()
	r.ready <- struct{}{}
	select {
	case <-ctx.Done():
		return delegation.Anchor{}, delegation.Result{}, ctx.Err()
	case <-r.release:
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	taskID := strings.TrimSpace(spawn.TaskID)
	return delegation.Anchor{SessionID: fmt.Sprintf("child-%d", id), Agent: req.Agent, AgentID: taskID}, delegation.Result{
		State: delegation.StateRunning, Running: true,
	}, nil
}

func (r *overlappingSubagentRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, nil
}

func (r *overlappingSubagentRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{State: delegation.StateRunning, Running: true}, nil
}

func (r *overlappingSubagentRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func (r *overlappingSubagentRunner) waitUntilOverlapping(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for i := 0; i < r.want; i++ {
		select {
		case <-r.ready:
		case <-timer.C:
			t.Fatalf("only %d/%d Spawn calls reached the external runner", i, r.want)
		}
	}
}

func (r *overlappingSubagentRunner) maxActive() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxSeen
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
	for index, input := range []map[string]any{
		{"prompt": "review this"},
		{"agent": "self", "prompt": "review this"},
	} {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		if _, err := targetTool.Call(ctx, tool.Call{ID: fmt.Sprintf("spawn-%d", index+1), Name: spawn.ToolName, Input: raw}); err != nil {
			t.Fatalf("SPAWN Call(%v) error = %v", input, err)
		}
		if runner.spawnTargetRequest.Target.Selector != "self" {
			t.Fatalf("spawn selector = %q, want self", runner.spawnTargetRequest.Target.Selector)
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
	if _, err := targetTool.Call(ctx, tool.Call{ID: "spawn-reviewer", Name: spawn.ToolName, Input: raw}); err != nil {
		t.Fatalf("SPAWN Call(reviewer) error = %v", err)
	}
	if runner.spawnTargetRequest.Target.Selector != "reviewer" {
		t.Fatalf("spawn selector = %q, want reviewer", runner.spawnTargetRequest.Target.Selector)
	}
}

func TestRuntimeSpawnToolPersistsResolvedPlacementBeforeSpawn(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	targetTool := runtimeSpawnTool{
		base: spawn.NewWithTargets(
			[]delegation.Agent{{Name: "self"}, {Name: "orbit"}},
			map[string]spawn.Target{
				"orbit": {
					Selector: "orbit",
					Placement: mustSealPlacement(t, delegation.Placement{
						Kind: delegation.PlacementModel, Model: "provider/model", ReasoningEffort: "high", ConfigFingerprint: "config-v1",
					}),
				},
			},
		),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
		runner:     runner,
	}
	raw, err := json.Marshal(map[string]any{"agent": "orbit", "prompt": "review this"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = targetTool.Call(ctx, tool.Call{ID: "spawn-placement", Name: spawn.ToolName, Input: raw})
	if err != nil {
		t.Fatalf("SPAWN Call() error = %v", err)
	}
	if runner.spawnTargetRequest.Target.Selector != "orbit" {
		t.Fatalf("runner selector = %q, want stable selector", runner.spawnTargetRequest.Target.Selector)
	}
	taskID := runner.spawnContext.TaskID
	entry, err := runtime.tasks.store.Get(ctx, strings.TrimSpace(taskID))
	if err != nil {
		t.Fatalf("Get(task) error = %v", err)
	}
	target := taskSpecTarget(entry.Spec, "target")
	if target.Selector != "orbit" || target.Placement.Model != "provider/model" || target.Placement.ReasoningEffort != "high" || !strings.HasPrefix(target.Placement.Fingerprint, "sha256:") {
		t.Fatalf("durable target = %#v", target)
	}
	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: taskID, Input: "follow up", Principal: session.ActorKindTool,
	}); err != nil {
		t.Fatalf("Write(placement task) error = %v", err)
	}
	if runner.continueAgent != "orbit" {
		t.Fatalf("continue Agent = %q, want stable selector", runner.continueAgent)
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
	if runner.spawnTargetRequest.Target.Selector != "self" {
		t.Fatalf("spawn selector = %q, want self", runner.spawnTargetRequest.Target.Selector)
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
	if _, err := runtime.sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: activeSession.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[approval.StateCurrentApprovalMode] = "manual"
		return next, nil
	}}); err != nil {
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
	sessions := inmemory.NewStore(inmemory.Config{})
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
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{},
		Subagents:    runner,
		TaskStore:    newFileTaskStoreForTest(t),
	}))
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
	spawnTargetRequest delegation.TargetRequest
	continueAnchor     delegation.Anchor
	continueAgent      string
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
	agentName := req.Agent
	return delegation.Anchor{SessionID: "child-1", Agent: agentName, AgentID: strings.TrimSpace(spawn.TaskID)}, delegation.CloneResult(r.spawnResult), nil
}

func (r *recordingSubagentRunner) SpawnTarget(ctx context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (delegation.Anchor, delegation.Result, error) {
	r.spawnTargetRequest = delegation.CloneTargetRequest(req)
	return r.Spawn(ctx, spawn, delegation.Request{Agent: req.Target.Selector, Prompt: req.Prompt})
}

func (r *recordingSubagentRunner) Continue(_ context.Context, anchor delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	r.continueAnchor = delegation.CloneAnchor(anchor)
	r.continueAgent = strings.TrimSpace(anchor.Agent)
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
