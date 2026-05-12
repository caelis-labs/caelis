package agent

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestNewContextProvidesStableReadonlyViews(t *testing.T) {
	t.Parallel()

	events := []*session.Event{
		{
			ID:      "ev-1",
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}
	state := map[string]any{"mode": "chat"}

	ctx := NewContext(ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName:      "caelis",
				UserID:       "user-1",
				SessionID:    "sess-1",
				WorkspaceKey: "ws-1",
			},
			CWD: "/tmp/project",
		},
		Events:  events,
		State:   state,
		Overlay: true,
	})

	if got := ctx.Session().SessionID; got != "sess-1" {
		t.Fatalf("SessionID = %q, want %q", got, "sess-1")
	}
	if got := ctx.Events().Len(); got != 1 {
		t.Fatalf("Events().Len() = %d, want 1", got)
	}
	if got := ctx.Events().At(0).Text; got != "hello" {
		t.Fatalf("Events().At(0).Text = %q, want %q", got, "hello")
	}
	if got, ok := ctx.ReadonlyState().Lookup("mode"); !ok || got != "chat" {
		t.Fatalf("ReadonlyState(mode) = %v, %v", got, ok)
	}
	if !ctx.Overlay() {
		t.Fatal("expected overlay context")
	}

	events[0].Text = "mutated"
	state["mode"] = "other"
	if got := ctx.Events().At(0).Text; got != "hello" {
		t.Fatalf("context should be isolated from source mutations, got %q", got)
	}
	if got, _ := ctx.ReadonlyState().Lookup("mode"); got != "chat" {
		t.Fatalf("readonly state should be isolated from source mutations, got %v", got)
	}
}

type staticTool struct {
	name string
	desc string
}

func (t staticTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        t.name,
		Description: t.desc,
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t staticTool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

func TestAgentSpecCarriesExecutionCapabilitiesOutsideContext(t *testing.T) {
	t.Parallel()

	model := staticModel{name: "stub"}
	inspectTool := staticTool{name: "inspect", desc: "inspect state"}
	runner := staticSubagentRunner{}

	spec := AgentSpec{
		Name:           "default",
		Model:          model,
		Tools:          []tool.Tool{inspectTool},
		SubagentRunner: runner,
		Metadata:       map[string]any{"mode": "chat"},
	}

	if got := spec.Name; got != "default" {
		t.Fatalf("spec.Name = %q, want %q", got, "default")
	}
	if got := spec.Model.Name(); got != "stub" {
		t.Fatalf("spec.Model.Name() = %q, want %q", got, "stub")
	}
	if got := len(spec.Tools); got != 1 {
		t.Fatalf("len(spec.Tools) = %d, want 1", got)
	}
	anchor, _, err := spec.SubagentRunner.Spawn(context.Background(), subagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "sess-1"},
		CWD:        "/tmp/project",
	}, delegation.Request{Agent: "helper"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if _, err := spec.SubagentRunner.Wait(context.Background(), anchor, 25); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got := spec.Metadata["mode"]; got != "chat" {
		t.Fatalf("spec.Metadata[mode] = %v, want %q", got, "chat")
	}
}

type staticModel struct {
	name string
}

func (m staticModel) Name() string { return m.name }

func (m staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}

type staticSubagentRunner struct{}

func (staticSubagentRunner) Spawn(context.Context, subagent.SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{TaskID: "task-sub-1", SessionID: "child-1", Agent: "helper", AgentID: "helper-1"}, delegation.Result{
		TaskID:        "task-sub-1",
		State:         delegation.StateRunning,
		OutputPreview: "helper started",
	}, nil
}

func (staticSubagentRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{TaskID: "task-sub-1", State: delegation.StateRunning, OutputPreview: "helper continued"}, nil
}

func (staticSubagentRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{TaskID: "task-sub-1", State: delegation.StateRunning, OutputPreview: "helper started"}, nil
}

func (staticSubagentRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func ptrMessage(message model.Message) *model.Message {
	return &message
}
