package gatewayapp

import (
	"context"
	"iter"
	"sort"
	"strings"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/ports/agentprofile"
)

func TestSystemManagedAgentUsesCoreRuntimeLifecycleAndJournalPipeline(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	interceptor := &systemManagedLifecycleRecorder{}
	guardrail := &systemManagedGuardrailRecorder{}
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:          chat.Factory{},
		StagingSessions:       func() session.Service { return staging },
		LifecycleInterceptors: []agent.LifecycleInterceptor{interceptor},
		Guardrails:            []agent.GuardrailSpec{{Guardrail: guardrail, OnFailure: agent.GuardrailFailClosed}},
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID: guardianProfileID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
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
	if guardrail.calls() != 1 {
		t.Fatalf("guardrail calls = %d, want one Core Runtime input pass", guardrail.calls())
	}
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID: guardianProfileID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := staging.Events(context.Background(), session.EventsRequest{SessionRef: plan.Session.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events(staging) error = %v", err)
	}
	wantTerminal := map[session.JournalKind]bool{session.JournalKindRun: false, session.JournalKindTurn: false}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := event.Journal.Execution
		if record.Status == session.ExecutionSucceeded {
			wantTerminal[record.Kind] = true
		}
	}
	for kind, seen := range wantTerminal {
		if !seen {
			t.Fatalf("staging events = %#v, want terminal %s journal", events, kind)
		}
	}
}

func TestSystemManagedAgentDoesNotInheritParentRuntimeLease(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:    chat.Factory{},
		StagingSessions: func() session.Service { return staging },
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	parentLease := session.SessionLease{
		SessionRef: parent.SessionRef,
		LeaseID:    "parent-lease", OwnerID: "parent-owner", FencingToken: 7,
	}
	ctx := session.ContextWithRuntimeLease(context.Background(), parentLease)
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID: guardianProfileID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &prompt, Text: "review this"}},
	})
	if err != nil {
		t.Fatalf("Run() inherited parent lease into staging Session: %v", err)
	}
	if result.AssistantEvent == nil {
		t.Fatal("Run() returned no Guardian assessment")
	}
}

type systemManagedLifecycleRecorder struct {
	mu     sync.Mutex
	events []agent.LifecycleEvent
}

type systemManagedGuardrailRecorder struct {
	mu    sync.Mutex
	count int
}

func (*systemManagedGuardrailRecorder) Name() string { return "system-managed-test" }

func (r *systemManagedGuardrailRecorder) ApplyGuardrail(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return input, nil
}

func (r *systemManagedGuardrailRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *systemManagedLifecycleRecorder) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return next(ctx)
}

func (r *systemManagedLifecycleRecorder) snapshot() []agent.LifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.LifecycleEvent(nil), r.events...)
}

func (r *systemManagedLifecycleRecorder) saw(operation agent.LifecycleOperation) bool {
	for _, event := range r.snapshot() {
		if event.Operation == operation {
			return true
		}
	}
	return false
}

func TestSystemManagedAgentRegistryEntries(t *testing.T) {
	specs := systemManagedAgentSpecs()
	if len(specs) != 1 {
		t.Fatalf("systemManagedAgentSpecs() len = %d, want 1: %#v", len(specs), specs)
	}
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("systemManagedAgentSpecs() ids = %#v, want sorted", ids)
	}

	spec := specs[0]
	if spec.ID != guardianProfileID {
		t.Fatalf("systemManagedAgentSpecs()[0].ID = %q, want guardian", spec.ID)
	}
	if spec.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian purpose = %q, want approval_review", spec.Purpose)
	}
	if spec.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("guardian capability = %q, want none", spec.CapabilityProfile)
	}
	if !spec.BindingPolicy.ForceEnabled || spec.BindingPolicy.AllowExternalACP {
		t.Fatalf("guardian binding policy = %#v, want forced enabled and no external ACP", spec.BindingPolicy)
	}
}

func TestSystemManagedAgentRegistryOwnsProfileAndBindingPolicy(t *testing.T) {
	spec, ok := systemManagedAgentSpecFor("guardian")
	if !ok {
		t.Fatal("systemManagedAgentSpecFor(guardian) missing")
	}
	if spec.ID != guardianProfileID || spec.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian spec = %#v, want approval-review registry entry", spec)
	}

	profile, ok := systemManagedAgentProfile("guardian")
	if !ok {
		t.Fatal("systemManagedAgentProfile(guardian) missing")
	}
	if profile.Name != "Guardian" {
		t.Fatalf("guardian profile name = %q, want Guardian", profile.Name)
	}
	if value, _ := profile.Metadata["system_managed"].(bool); !value {
		t.Fatalf("guardian profile metadata = %#v, want system_managed", profile.Metadata)
	}

	disabled := false
	binding := normalizeSystemManagedAgentBinding(spec, agentprofile.Binding{
		ProfileID: "guardian",
		Target:    agentprofile.BindingTargetACP,
		ACPAgent:  "legacy-reviewer",
		ACPModel:  "legacy-model",
		Enabled:   &disabled,
		Status:    agentprofile.BindingStatusStale,
		Warning:   "legacy binding",
	})
	if binding.Enabled == nil || !*binding.Enabled {
		t.Fatalf("normalized guardian binding enabled = %#v, want forced enabled", binding.Enabled)
	}
	if binding.Target == agentprofile.BindingTargetACP || binding.ACPAgent != "" || binding.ACPModel != "" {
		t.Fatalf("normalized guardian binding = %#v, want ACP binding cleared", binding)
	}
	if binding.Status != agentprofile.BindingStatusOK || binding.Warning != "" {
		t.Fatalf("normalized guardian binding status = %q warning %q, want clean status", binding.Status, binding.Warning)
	}

	err := validateSystemManagedAgentBinding(spec, agentprofile.Binding{
		ProfileID: "guardian",
		Target:    agentprofile.BindingTargetACP,
		ACPAgent:  "reviewer",
		Enabled:   boolPtr(true),
	})
	if err == nil || !strings.Contains(err.Error(), "guardian cannot bind to an external ACP agent") {
		t.Fatalf("validateSystemManagedAgentBinding() error = %v, want guardian ACP rejection", err)
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
	if plan.Spec.ID != guardianProfileID || plan.AgentID != guardianProfileID {
		t.Fatalf("run plan agent = spec %q id %q, want guardian", plan.Spec.ID, plan.AgentID)
	}
	if plan.Purpose != systemManagedAgentPurposeApprovalReview || plan.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("run plan purpose/capability = %q/%q, want approval_review/none", plan.Purpose, plan.CapabilityProfile)
	}
	if got := plan.Metadata["system_managed_agent"]; got != guardianProfileID {
		t.Fatalf("run plan metadata system_managed_agent = %#v, want guardian", got)
	}
	if got := plan.Metadata["system_managed_capability_profile"]; got != string(systemManagedAgentCapabilityNone) {
		t.Fatalf("run plan metadata capability = %#v, want none", got)
	}
	if plan.Session.SessionID != "parent-session-approval-review" {
		t.Fatalf("run plan session id = %q, want parent-session-approval-review", plan.Session.SessionID)
	}
	if got := plan.Session.Metadata["system_managed_agent"]; got != guardianProfileID {
		t.Fatalf("run plan session metadata system_managed_agent = %#v, want guardian", got)
	}
}

type systemManagedAgentTestModel struct {
	name string
}

type systemManagedAgentResponseModel struct{}

func (systemManagedAgentResponseModel) Name() string { return "system-managed-response" }

func (systemManagedAgentResponseModel) Capabilities() model.Capabilities {
	return model.Capabilities{StructuredOutput: true}
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
	return model.Capabilities{StructuredOutput: true}
}

func (m systemManagedAgentTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
