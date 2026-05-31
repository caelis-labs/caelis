package local

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

func TestControllerRunManagerReportsRunningAndFailedLifecycle(t *testing.T) {
	ctx := context.Background()
	manager := newControllerRunManager(storememory.New(), []acpexternal.Config{{
		AgentID: "reviewer",
		Command: "reviewer-acp",
	}}, t.TempDir())
	if manager == nil {
		t.Fatal("controller run manager = nil")
	}
	tracker := manager.tracker()
	now := time.Date(2026, 5, 31, 11, 0, 0, 0, time.UTC)
	controller := session.ControllerBinding{
		Kind:      session.ControllerACP,
		ID:        "reviewer",
		AgentName: "reviewer",
		EpochID:   "controller-1",
	}
	state := control.ControllerInvocationState{
		Phase:      control.ControllerInvocationStarted,
		SessionRef: session.Ref{SessionID: "sess-controller"},
		TurnID:     "turn-1",
		Controller: controller,
		Input:      "inspect",
		Time:       now,
	}
	if err := tracker.ControllerInvocationChanged(ctx, state); err != nil {
		t.Fatal(err)
	}
	runs, err := manager.ControllerRuns(ctx, services.ControllerRunQuery{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Controller: controller,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].Running || !runs[0].Active || runs[0].Phase != control.ControllerInvocationStarted || runs[0].TurnID != "turn-1" {
		t.Fatalf("running controller runs = %#v, want active started run", runs)
	}

	state.Phase = control.ControllerInvocationRemoteSession
	state.RemoteSessionID = "remote-reviewer"
	state.Time = now.Add(time.Second)
	if err := tracker.ControllerInvocationChanged(ctx, state); err != nil {
		t.Fatal(err)
	}
	runs, err = manager.ControllerRuns(ctx, services.ControllerRunQuery{SessionRef: session.Ref{SessionID: "sess-controller"}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].RemoteSessionID != "remote-reviewer" || runs[0].Phase != control.ControllerInvocationRemoteSession {
		t.Fatalf("remote controller runs = %#v, want remote session phase", runs)
	}

	state.Phase = control.ControllerInvocationFailed
	state.Error = "remote process failed"
	state.Time = now.Add(2 * time.Second)
	if err := tracker.ControllerInvocationChanged(ctx, state); err != nil {
		t.Fatal(err)
	}
	runs, err = manager.ControllerRuns(ctx, services.ControllerRunQuery{SessionRef: session.Ref{SessionID: "sess-controller"}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Running || runs[0].Active || runs[0].Phase != control.ControllerInvocationFailed || runs[0].Error != "remote process failed" {
		t.Fatalf("failed controller runs = %#v, want retained failed diagnostic", runs)
	}
	running, err := manager.journal.readRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Fatalf("running journal records = %#v, want failed record excluded from recovery", running)
	}
}

func TestControllerRunManagerRetainsRecoveryStartFailure(t *testing.T) {
	ctx := context.Background()
	manager := newControllerRunManager(storememory.New(), []acpexternal.Config{{
		AgentID: "reviewer",
		Command: "caelis-test-missing-acp-command",
	}}, t.TempDir())
	if manager == nil {
		t.Fatal("controller run manager = nil")
	}
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	record := controllerRunJournalRecord{
		ID:         "run-recovery",
		SessionRef: session.Ref{SessionID: "sess-controller"},
		TurnID:     "turn-recovery",
		Controller: session.ControllerBinding{
			Kind:      session.ControllerACP,
			ID:        "reviewer",
			AgentName: "reviewer",
		},
		Input:     "resume",
		Running:   true,
		Phase:     control.ControllerInvocationStarted,
		StartedAt: now,
		UpdatedAt: now,
	}
	if !manager.markActive(record.ID, true) {
		t.Fatal("markActive returned false")
	}
	manager.recover(ctx, record, manager.configs[0])

	runs, err := manager.ControllerRuns(ctx, services.ControllerRunQuery{
		SessionRef: session.Ref{SessionID: "sess-controller"},
		Controller: record.Controller,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Running || runs[0].Active || runs[0].Phase != control.ControllerInvocationFailed || runs[0].Error == "" {
		t.Fatalf("recovery failure runs = %#v, want retained failed lifecycle diagnostic", runs)
	}
}
