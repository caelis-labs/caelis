package agentsdk

import (
	"context"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestNewContextProvidesStableEventSnapshot(t *testing.T) {
	t.Parallel()

	events := []*session.Event{
		{
			ID:      "ev-1",
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}

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
		Events: events,
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
	events[0].Text = "mutated"
	if got := ctx.Events().At(0).Text; got != "hello" {
		t.Fatalf("context should be isolated from source mutations, got %q", got)
	}
}

func TestInputReadyIsScopedToInvocationAndSurvivesContextWrapping(t *testing.T) {
	ready := make(chan struct{})
	parent := NewContext(ContextSpec{Context: t.Context(), InputReady: func() <-chan struct{} { return ready }})
	wrapped, cancel := context.WithCancel(parent)
	defer cancel()
	if got := InputReady(wrapped); got != ready {
		t.Fatal("tool context wrapper lost the invocation's input signal")
	}
	child := NewContext(ContextSpec{Context: wrapped})
	if got := InputReady(child); got != nil {
		t.Fatal("new invocation inherited the parent's input signal")
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

	spec := AgentSpec{
		Name:     "default",
		Model:    model,
		Tools:    []tool.Tool{inspectTool},
		Metadata: map[string]any{"mode": "chat"},
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
	if got := spec.Metadata["mode"]; got != "chat" {
		t.Fatalf("spec.Metadata[mode] = %v, want %q", got, "chat")
	}
}

func TestModelRequestOptionsWithDefaultsCopiesServiceTier(t *testing.T) {
	t.Parallel()

	priority := ModelRequestOptions{ServiceTier: model.ServiceTierPriority}
	if got := (ModelRequestOptions{}).WithDefaults(priority); got.ServiceTier != model.ServiceTierPriority {
		t.Fatalf("empty overlay ServiceTier = %q, want default priority", got.ServiceTier)
	}
	if got := priority.WithDefaults(ModelRequestOptions{}); got.ServiceTier != model.ServiceTierPriority {
		t.Fatalf("overlay ServiceTier = %q, want priority", got.ServiceTier)
	}
	if got := (ModelRequestOptions{}).WithDefaults(ModelRequestOptions{}); got.ServiceTier != "" {
		t.Fatalf("empty ServiceTier = %q, want omitted", got.ServiceTier)
	}
}

type staticModel struct {
	name string
}

func (m staticModel) Name() string { return m.name }

func (m staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}
