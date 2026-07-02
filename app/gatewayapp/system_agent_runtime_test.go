package gatewayapp

import (
	"context"
	"iter"
	"sort"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

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

func (m systemManagedAgentTestModel) Name() string {
	if strings.TrimSpace(m.name) != "" {
		return strings.TrimSpace(m.name)
	}
	return "system-managed-test-model"
}

func (m systemManagedAgentTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
