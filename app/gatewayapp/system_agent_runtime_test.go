package gatewayapp

import (
	"context"
	"iter"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSystemManagedAgentUsesCoreRuntimeHooksAndFacts(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	interceptor := &systemManagedLifecycleRecorder{}
	startHook := &systemManagedStartRecorder{}
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:    chat.Factory{},
		StagingSessions: func() session.Service { return staging },
		TraceSink:       interceptor,
		Hooks:           agent.Hooks{TurnStart: []agent.Hook[struct{}, agent.TurnStartDecision]{{Name: "start", Apply: startHook.Apply}}},
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &prompt, Text: "review this"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.AssistantEvent == nil || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("Run() result = %#v, want assistant assessment", result)
	}
	for _, operation := range []agent.LifecycleOperation{agent.LifecycleRun, agent.LifecycleTurn, agent.LifecycleModel} {
		if !interceptor.saw(operation) {
			t.Fatalf("lifecycle events = %#v, want %q", interceptor.snapshot(), operation)
		}
	}
	if startHook.calls() != 1 {
		t.Fatalf("startHook calls = %d, want one Core Runtime input pass", startHook.calls())
	}
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := staging.Events(context.Background(), session.EventsRequest{SessionRef: plan.Session.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events(staging) error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("private staging facts missing")
	}
	for _, event := range events {
		if event.Approval != nil {
			t.Fatal("unexpected approval")
		}
	}

}

func TestSystemManagedAgentDoesNotInheritParentRuntimeFence(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:    chat.Factory{},
		StagingSessions: func() session.Service { return staging },
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	parentFence := session.SessionFence{
		SessionRef: parent.SessionRef,
		FenceID:    "parent-fence", OwnerID: "parent-owner", FencingToken: 7,
	}
	ctx := session.ContextWithRuntimeFence(context.Background(), parentFence)
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &prompt, Text: "review this"}},
	})
	if err != nil {
		t.Fatalf("Run() inherited parent fence into staging Session: %v", err)
	}
	if result.AssistantEvent == nil {
		t.Fatal("Run() returned no Guardian assessment")
	}
}

type systemManagedLifecycleRecorder struct {
	mu     sync.Mutex
	events []agent.LifecycleEvent
}

type systemManagedStartRecorder struct {
	mu    sync.Mutex
	count int
}

func (r *systemManagedStartRecorder) Apply(_ context.Context, _ agent.HookContext, _ struct{}) (agent.TurnStartDecision, error) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return agent.TurnStartDecision{}, nil
}

func (r *systemManagedStartRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *systemManagedLifecycleRecorder) RecordTrace(record agent.TraceRecord) {
	r.mu.Lock()
	r.events = append(r.events, record.Event)
	r.mu.Unlock()
}

func (r *systemManagedLifecycleRecorder) snapshot() []agent.LifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.LifecycleEvent(nil), r.events...)
}

func (r *systemManagedLifecycleRecorder) saw(operation agent.LifecycleOperation) bool {
	deadline := time.Now().Add(time.Second)
	for {
		for _, event := range r.snapshot() {
			if event.Operation == operation {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSystemManagedAgentRegistryEntries(t *testing.T) {
	specs := systemManagedAgentSpecs()
	if len(specs) != 1 {
		t.Fatalf("systemManagedAgentSpecs() len = %d, want only Guardian: %#v", len(specs), specs)
	}
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("systemManagedAgentSpecs() ids = %#v, want sorted", ids)
	}

	guardian, ok := systemManagedAgentSpecFor(guardianSceneID)
	if !ok {
		t.Fatal("guardian system Agent is missing")
	}
	if guardian.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian purpose = %q, want approval_review", guardian.Purpose)
	}
	if guardian.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("guardian capability = %q, want none", guardian.CapabilityProfile)
	}
}

func TestSystemManagedAgentRegistryOwnsFixedScene(t *testing.T) {
	spec, ok := systemManagedAgentSpecFor("guardian")
	if !ok {
		t.Fatal("systemManagedAgentSpecFor(guardian) missing")
	}
	if spec.ID != guardianSceneID || spec.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian spec = %#v, want approval-review registry entry", spec)
	}

	if spec.Instructions == "" || spec.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("guardian scene = %#v, want fixed instructions without tools", spec)
	}
}

func TestSystemManagedAgentRunPlanUsesRegistryDefaults(t *testing.T) {
	parent := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "user-1",
			SessionID:    "parent-session",
			WorkspaceKey: "workspace-1",
		},
		CWD: "/tmp/workspace",
	}
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID:       "guardian",
		Model:         systemManagedAgentTestModel{name: "guardian-model"},
		ParentSession: parent,
	})
	if err != nil {
		t.Fatalf("systemManagedAgentRunPlanFor() error = %v", err)
	}
	if plan.Spec.ID != guardianSceneID || plan.AgentID != guardianSceneID {
		t.Fatalf("run plan agent = spec %q id %q, want guardian", plan.Spec.ID, plan.AgentID)
	}
	if plan.Purpose != systemManagedAgentPurposeApprovalReview || plan.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("run plan purpose/capability = %q/%q, want approval_review/none", plan.Purpose, plan.CapabilityProfile)
	}
	if got := plan.Metadata["system_managed_agent"]; got != guardianSceneID {
		t.Fatalf("run plan metadata system_managed_agent = %#v, want guardian", got)
	}
	if got := plan.Metadata["system_managed_capability_profile"]; got != string(systemManagedAgentCapabilityNone) {
		t.Fatalf("run plan metadata capability = %#v, want none", got)
	}
	if plan.Session.SessionID != "parent-session-approval-review" {
		t.Fatalf("run plan session id = %q, want parent-session-approval-review", plan.Session.SessionID)
	}
	if got := plan.Session.Metadata["system_managed_agent"]; got != guardianSceneID {
		t.Fatalf("run plan session metadata system_managed_agent = %#v, want guardian", got)
	}
}

type systemManagedAgentTestModel struct {
	name string
}

type systemManagedAgentResponseModel struct{}

func (systemManagedAgentResponseModel) Name() string { return "system-managed-response" }

func (systemManagedAgentResponseModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (systemManagedAgentResponseModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true,
			Message: model.NewTextMessage(model.RoleAssistant, `{"outcome":"allow"}`),
		}}, nil)
	}
}

func (m systemManagedAgentTestModel) Name() string {
	if strings.TrimSpace(m.name) != "" {
		return strings.TrimSpace(m.name)
	}
	return "system-managed-test-model"
}

func (m systemManagedAgentTestModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (m systemManagedAgentTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
