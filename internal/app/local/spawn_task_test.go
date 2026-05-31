package local

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
)

func TestSpawnTaskManagerContinuesRecoveredRemoteTask(t *testing.T) {
	ctx := context.Background()
	store := storememory.New()
	parent, err := store.Create(ctx, session.StartRequest{
		AppName: "caelis",
		UserID:  "tester",
		Workspace: session.Workspace{
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	startedAt := time.Now().UTC().Add(-time.Minute)
	journal := newSpawnTaskJournal(stateDir)
	if err := journal.write(ctx, spawnTaskJournalRecord{
		Parent:          parent.Ref,
		Workspace:       parent.Workspace,
		TurnID:          "turn-1",
		Agent:           "helper",
		RemoteSessionID: "remote-reconnect",
		Snapshot: sandbox.SessionSnapshot{
			Ref:       sandbox.SessionRef{ID: "spawn-call", Backend: sandbox.BackendCustom},
			Command:   "SPAWN helper",
			State:     sandbox.SessionRunning,
			Running:   true,
			ExitCode:  -1,
			StartedAt: startedAt,
			UpdatedAt: startedAt,
			Terminal:  sandbox.TerminalRef{ID: "spawn-spawn-call", SessionID: "spawn-call"},
			Metadata: map[string]any{
				"task_kind":         "subagent",
				"source":            "spawn",
				"agent":             "helper",
				"remote_session_id": "remote-reconnect",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	manager := newSpawnTaskManager(store, []acpexternal.Config{{
		AgentID:   "helper",
		AgentName: "helper",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
		Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
	}}, stateDir)
	opened, ok, err := manager.OpenTask(ctx, sandbox.SessionRef{ID: "spawn-call"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("OpenTask() ok = false, want recovered SPAWN task")
	}
	snapshot, err := opened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Running || !snapshot.SupportsInput || snapshot.Metadata["reconnectable"] != true {
		t.Fatalf("recovered snapshot = %#v, want stopped reconnectable task", snapshot)
	}

	if err := opened.Write(ctx, []byte("continue after restart")); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := opened.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "external helper response") {
		t.Fatalf("wait stdout = %q, want helper response", result.Stdout)
	}
	finalSnapshot, err := opened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if finalSnapshot.State != sandbox.SessionCompleted || !finalSnapshot.SupportsInput {
		t.Fatalf("final snapshot = %#v, want completed reconnectable task", finalSnapshot)
	}

	stored, err := store.Load(ctx, parent.Ref)
	if err != nil {
		t.Fatal(err)
	}
	childEvent := findSubagentEvent(stored.Events, "spawn-call")
	if childEvent == nil {
		t.Fatalf("stored events = %#v, want canonical subagent event", stored.Events)
	}
	if childEvent.Scope.Participant.SessionID != "remote-reconnect" {
		t.Fatalf("participant session id = %q, want remote-reconnect", childEvent.Scope.Participant.SessionID)
	}
	if lifecycle := findTaskLifecycleEvent(stored.Events, "spawn-call", "write"); lifecycle == nil || lifecycle.Lifecycle == nil || lifecycle.Lifecycle.Status != session.LifecycleRunning {
		t.Fatalf("stored events = %#v, want running write lifecycle for continued SPAWN task", stored.Events)
	}
	if lifecycle := findTaskLifecycleEvent(stored.Events, "spawn-call", "completed"); lifecycle == nil || lifecycle.Lifecycle == nil || lifecycle.Lifecycle.Status != session.LifecycleCompleted {
		t.Fatalf("stored events = %#v, want completed lifecycle for continued SPAWN task", stored.Events)
	}
}

func TestSpawnTaskManagerResumesRunningJournalPrompt(t *testing.T) {
	ctx := context.Background()
	store := storememory.New()
	parent, err := store.Create(ctx, session.StartRequest{
		AppName: "caelis",
		UserID:  "tester",
		Workspace: session.Workspace{
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	startedAt := time.Now().UTC().Add(-time.Minute)
	journal := newSpawnTaskJournal(stateDir)
	if err := journal.write(ctx, spawnTaskJournalRecord{
		Parent:          parent.Ref,
		Workspace:       parent.Workspace,
		TurnID:          "turn-live",
		Agent:           "helper",
		RemoteSessionID: "remote-live",
		PendingPrompt:   "continue the interrupted prompt",
		Snapshot: sandbox.SessionSnapshot{
			Ref:       sandbox.SessionRef{ID: "spawn-live", Backend: sandbox.BackendCustom},
			Command:   "SPAWN helper",
			State:     sandbox.SessionRunning,
			Running:   true,
			ExitCode:  -1,
			StartedAt: startedAt,
			UpdatedAt: startedAt,
			Terminal:  sandbox.TerminalRef{ID: "spawn-spawn-live", SessionID: "spawn-live"},
			Metadata: map[string]any{
				"task_kind":         "subagent",
				"source":            "spawn",
				"agent":             "helper",
				"remote_session_id": "remote-live",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	manager := newSpawnTaskManager(store, []acpexternal.Config{{
		AgentID:   "helper",
		AgentName: "helper",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
		Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
	}}, stateDir)
	listed, err := manager.ListTasks(ctx, sandbox.SessionListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSandboxSnapshot(listed, "spawn-live") {
		t.Fatalf("listed tasks = %#v, want recovered running SPAWN task", listed)
	}
	opened, ok, err := manager.OpenTask(ctx, sandbox.SessionRef{ID: "spawn-live"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("OpenTask() ok = false, want live recovered SPAWN task")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := opened.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "external helper response") {
		t.Fatalf("wait stdout = %q, want resumed helper response", result.Stdout)
	}
	snapshot, err := opened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != sandbox.SessionCompleted || snapshot.Running || !snapshot.SupportsInput {
		t.Fatalf("resumed snapshot = %#v, want completed reconnectable task", snapshot)
	}
	record, err := journal.read("spawn-live")
	if err != nil {
		t.Fatal(err)
	}
	if record.PendingPrompt != "" || record.Snapshot.Running {
		t.Fatalf("journal record = %#v, want completed record with cleared pending prompt", record)
	}

	stored, err := store.Load(ctx, parent.Ref)
	if err != nil {
		t.Fatal(err)
	}
	childEvent := findSubagentEvent(stored.Events, "spawn-live")
	if childEvent == nil {
		t.Fatalf("stored events = %#v, want resumed canonical subagent event", stored.Events)
	}
	if childEvent.Scope.Participant.SessionID != "remote-live" {
		t.Fatalf("participant session id = %q, want remote-live", childEvent.Scope.Participant.SessionID)
	}
	if lifecycle := findTaskLifecycleEvent(stored.Events, "spawn-live", "completed"); lifecycle == nil || lifecycle.Lifecycle == nil || lifecycle.Lifecycle.Status != session.LifecycleCompleted {
		t.Fatalf("stored events = %#v, want completed lifecycle for recovered SPAWN task", stored.Events)
	}
}

func hasSandboxSnapshot(items []sandbox.SessionSnapshot, id string) bool {
	for _, item := range items {
		if item.Ref.ID == id {
			return true
		}
	}
	return false
}
