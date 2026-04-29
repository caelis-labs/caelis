package runtime

import (
	"context"
	"iter"
	"testing"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestNewContextProvidesStableReadonlyViews(t *testing.T) {
	t.Parallel()

	events := []*sdksession.Event{
		{
			ID:      "ev-1",
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
			Text:    "hello",
		},
	}
	state := map[string]any{"mode": "chat"}

	ctx := NewContext(ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
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

func (t staticTool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        t.name,
		Description: t.desc,
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t staticTool) Call(context.Context, sdktool.Call) (sdktool.Result, error) {
	return sdktool.Result{}, nil
}

func TestAgentSpecCarriesExecutionCapabilitiesOutsideContext(t *testing.T) {
	t.Parallel()

	model := staticModel{name: "stub"}
	tool := staticTool{name: "inspect", desc: "inspect state"}
	runner := staticSubagentRunner{}

	spec := AgentSpec{
		Name:           "default",
		Model:          model,
		Tools:          []sdktool.Tool{tool},
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
	anchor, _, err := spec.SubagentRunner.Spawn(context.Background(), sdksubagent.SpawnContext{
		SessionRef: sdksession.SessionRef{SessionID: "sess-1"},
		CWD:        "/tmp/project",
	}, sdkdelegation.Request{Agent: "helper"})
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

func (m staticModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(func(*sdkmodel.StreamEvent, error) bool) {}
}

type staticSubagentRunner struct{}

func (staticSubagentRunner) Spawn(context.Context, sdksubagent.SpawnContext, sdkdelegation.Request) (sdkdelegation.Anchor, sdkdelegation.Result, error) {
	return sdkdelegation.Anchor{TaskID: "task-sub-1", SessionID: "child-1", Agent: "helper", AgentID: "helper-1"}, sdkdelegation.Result{
		TaskID:        "task-sub-1",
		State:         sdkdelegation.StateRunning,
		OutputPreview: "helper started",
	}, nil
}

func (staticSubagentRunner) Continue(context.Context, sdkdelegation.Anchor, sdkdelegation.ContinueRequest) (sdkdelegation.Result, error) {
	return sdkdelegation.Result{TaskID: "task-sub-1", State: sdkdelegation.StateRunning, OutputPreview: "helper continued"}, nil
}

func (staticSubagentRunner) Wait(context.Context, sdkdelegation.Anchor, int) (sdkdelegation.Result, error) {
	return sdkdelegation.Result{TaskID: "task-sub-1", State: sdkdelegation.StateRunning, OutputPreview: "helper started"}, nil
}

func (staticSubagentRunner) Cancel(context.Context, sdkdelegation.Anchor) error { return nil }

func ptrMessage(message sdkmodel.Message) *sdkmodel.Message {
	return &message
}
