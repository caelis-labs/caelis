package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/impl/agent/local/chat"
	"github.com/caelis-labs/caelis/ports/agent"
	"github.com/caelis-labs/caelis/ports/agentprofile"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/ports/tool"
)

type systemManagedAgentPurpose string

const (
	systemManagedAgentPurposeApprovalReview systemManagedAgentPurpose = "approval_review"
)

type systemManagedAgentCapabilityProfile string

const (
	systemManagedAgentCapabilityNone systemManagedAgentCapabilityProfile = "none"
	// ReadOnly and Controller are reserved capability cuts for future
	// system-managed agents. Guardian uses None so approval review cannot
	// receive runtime tools.
	systemManagedAgentCapabilityReadOnly   systemManagedAgentCapabilityProfile = "read_only"
	systemManagedAgentCapabilityController systemManagedAgentCapabilityProfile = "controller"
)

type systemManagedAgentBindingPolicy struct {
	ForceEnabled     bool
	AllowExternalACP bool
}

// systemManagedAgentSpec describes one built-in system-owned agent profile and
// its default runtime cuts. It is intentionally app-private until another layer
// needs a stable public system-agent contract.
type systemManagedAgentSpec struct {
	ID                string
	Name              string
	Description       string
	Capabilities      []string
	Instructions      string
	ProfileMetadata   map[string]any
	SessionID         func(session.Session, map[string]any) string
	SessionSuffix     string
	SessionMetadata   map[string]any
	Purpose           systemManagedAgentPurpose
	CapabilityProfile systemManagedAgentCapabilityProfile
	BindingPolicy     systemManagedAgentBindingPolicy
	ReasoningEffort   string
	Tools             []tool.Tool
}

// systemManagedAgentRunRequest is the narrow app-owned construction input for
// one system-managed agent invocation. Domain callers supply context and output
// contracts; this layer resolves the concrete runtime plan and capability cut.
type systemManagedAgentRunRequest struct {
	AgentID           string
	Purpose           systemManagedAgentPurpose
	Model             model.LLM
	ParentSession     session.Session
	Events            []*session.Event
	Tools             []tool.Tool
	Output            *model.OutputSpec
	Metadata          map[string]any
	CapabilityProfile systemManagedAgentCapabilityProfile
}

type systemManagedAgentRunResult struct {
	Events         []*session.Event
	AssistantEvent *session.Event
	Text           string
}

// systemManagedAgentRunPlan is the normalized runtime plan after applying the
// agent spec defaults, purpose, capability profile, session projection, and
// metadata used by the underlying Agent runtime.
type systemManagedAgentRunPlan struct {
	Spec              systemManagedAgentSpec
	AgentID           string
	Purpose           systemManagedAgentPurpose
	CapabilityProfile systemManagedAgentCapabilityProfile
	Model             model.LLM
	Session           session.Session
	Events            []*session.Event
	Tools             []tool.Tool
	Output            *model.OutputSpec
	Metadata          map[string]any
}

type systemManagedAgentRunner interface {
	Run(context.Context, systemManagedAgentRunRequest) (systemManagedAgentRunResult, error)
}

type systemManagedAgentRuntime struct {
	factory agent.AgentFactory
}

type systemManagedAgentRegistry struct {
	byID       map[string]systemManagedAgentSpec
	orderedIDs []string
}

var (
	systemManagedAgentRegistryOnce  sync.Once
	systemManagedAgentRegistryValue systemManagedAgentRegistry
)

func newSystemManagedAgentRuntime(factory agent.AgentFactory) *systemManagedAgentRuntime {
	if factory == nil {
		factory = chat.Factory{}
	}
	return &systemManagedAgentRuntime{factory: factory}
}

func (r *systemManagedAgentRuntime) Run(ctx context.Context, req systemManagedAgentRunRequest) (systemManagedAgentRunResult, error) {
	plan, err := systemManagedAgentRunPlanFor(req)
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	factory := agent.AgentFactory(nil)
	if r != nil {
		factory = r.factory
	}
	if factory == nil {
		factory = chat.Factory{}
	}
	metadata := chat.Metadata(plan.Spec.Instructions)
	if metadata == nil {
		metadata = map[string]any{}
	}
	for key, value := range plan.Metadata {
		metadata[key] = value
	}
	if strings.TrimSpace(plan.Spec.ReasoningEffort) != "" {
		metadata["reasoning_effort"] = strings.TrimSpace(plan.Spec.ReasoningEffort)
	}
	runtimeAgent, err := factory.NewAgent(ctx, agent.AgentSpec{
		Name:  plan.AgentID,
		Model: plan.Model,
		Tools: plan.Tools,
		Request: agent.ModelRequestOptions{
			Stream: boolPtr(false),
			Output: plan.Output,
		},
		Metadata: metadata,
	})
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context: ctx,
		Session: plan.Session,
		Events:  plan.Events,
	})
	result := systemManagedAgentRunResult{}
	for event, runErr := range runtimeAgent.Run(runCtx) {
		if runErr != nil {
			return result, runErr
		}
		if event == nil {
			continue
		}
		cloned := session.CloneEvent(event)
		result.Events = append(result.Events, cloned)
		if session.EventTypeOf(cloned) == session.EventTypeAssistant {
			result.AssistantEvent = cloned
		}
	}
	if result.AssistantEvent != nil {
		result.Text = session.EventText(result.AssistantEvent)
	}
	return result, nil
}

func systemManagedAgentRunPlanFor(req systemManagedAgentRunRequest) (systemManagedAgentRunPlan, error) {
	spec, ok := systemManagedAgentSpecFor(req.AgentID)
	if !ok {
		return systemManagedAgentRunPlan{}, fmt.Errorf("gatewayapp: unknown system-managed agent %q", strings.TrimSpace(req.AgentID))
	}
	if req.Model == nil {
		return systemManagedAgentRunPlan{}, fmt.Errorf("gatewayapp: system-managed agent %q requires a model", spec.ID)
	}
	purpose := req.Purpose
	if purpose == "" {
		purpose = spec.Purpose
	}
	if purpose == "" {
		purpose = systemManagedAgentPurpose(strings.TrimSpace(spec.ID))
	}
	capabilityProfile := req.CapabilityProfile
	if capabilityProfile == "" {
		capabilityProfile = spec.CapabilityProfile
	}
	if capabilityProfile == "" {
		capabilityProfile = systemManagedAgentCapabilityNone
	}
	tools, err := systemManagedAgentToolsForCapability(spec, req.Tools, capabilityProfile)
	if err != nil {
		return systemManagedAgentRunPlan{}, err
	}
	metadata := maps.Clone(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["system_managed_agent"] = strings.TrimSpace(spec.ID)
	metadata["system_managed_purpose"] = strings.TrimSpace(string(purpose))
	metadata["system_managed_capability_profile"] = strings.TrimSpace(string(capabilityProfile))
	sessionMetadata := maps.Clone(req.Metadata)
	if sessionMetadata == nil {
		sessionMetadata = map[string]any{}
	}
	sessionMetadata["system_managed_purpose"] = strings.TrimSpace(string(purpose))
	return systemManagedAgentRunPlan{
		Spec:              spec,
		AgentID:           strings.TrimSpace(spec.ID),
		Purpose:           purpose,
		CapabilityProfile: capabilityProfile,
		Model:             req.Model,
		Session:           systemManagedAgentSessionForParent(req.ParentSession, spec, sessionMetadata),
		Events:            session.CloneEvents(req.Events),
		Tools:             tools,
		Output:            req.Output,
		Metadata:          metadata,
	}, nil
}

func systemManagedAgentSpecs() []systemManagedAgentSpec {
	registry := systemManagedAgentRegistrySnapshot()
	out := make([]systemManagedAgentSpec, 0, len(registry.orderedIDs))
	for _, id := range registry.orderedIDs {
		out = append(out, registry.byID[id])
	}
	return out
}

func systemManagedAgentRegistrySnapshot() systemManagedAgentRegistry {
	systemManagedAgentRegistryOnce.Do(func() {
		systemManagedAgentRegistryValue = buildSystemManagedAgentRegistry([]systemManagedAgentSpec{
			guardianSystemManagedAgentSpec(),
		})
	})
	return systemManagedAgentRegistryValue
}

func buildSystemManagedAgentRegistry(specs []systemManagedAgentSpec) systemManagedAgentRegistry {
	byID := make(map[string]systemManagedAgentSpec, len(specs))
	for _, spec := range specs {
		spec = normalizeSystemManagedAgentSpec(spec)
		if spec.ID == "" {
			continue
		}
		byID[spec.ID] = spec
	}
	orderedIDs := make([]string, 0, len(byID))
	for id := range byID {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	return systemManagedAgentRegistry{byID: byID, orderedIDs: orderedIDs}
}

func normalizeSystemManagedAgentSpec(spec systemManagedAgentSpec) systemManagedAgentSpec {
	spec.ID = normalizeAgentProfileID(spec.ID)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Capabilities = append([]string(nil), spec.Capabilities...)
	spec.ProfileMetadata = maps.Clone(spec.ProfileMetadata)
	spec.SessionMetadata = maps.Clone(spec.SessionMetadata)
	spec.Tools = append([]tool.Tool(nil), spec.Tools...)
	return spec
}

func systemManagedAgentToolsForCapability(
	spec systemManagedAgentSpec,
	requestTools []tool.Tool,
	profile systemManagedAgentCapabilityProfile,
) ([]tool.Tool, error) {
	switch profile {
	case systemManagedAgentCapabilityNone:
		if len(spec.Tools) > 0 || len(requestTools) > 0 {
			return nil, fmt.Errorf("gatewayapp: system-managed agent %q capability profile %q does not allow tools", spec.ID, profile)
		}
		return nil, nil
	case systemManagedAgentCapabilityReadOnly, systemManagedAgentCapabilityController:
		tools := append([]tool.Tool(nil), spec.Tools...)
		return append(tools, requestTools...), nil
	default:
		return nil, fmt.Errorf("gatewayapp: system-managed agent %q has unsupported capability profile %q", spec.ID, profile)
	}
}

func systemManagedAgentProfile(agentID string) (agentprofile.Profile, bool) {
	spec, ok := systemManagedAgentSpecFor(agentID)
	if !ok {
		return agentprofile.Profile{}, false
	}
	return systemManagedAgentProfileFromSpec(spec), true
}

func systemManagedAgentProfileFromSpec(spec systemManagedAgentSpec) agentprofile.Profile {
	return agentprofile.NormalizeProfile(agentprofile.Profile{
		ID:           spec.ID,
		Name:         spec.Name,
		Description:  spec.Description,
		Capabilities: append([]string(nil), spec.Capabilities...),
		Instructions: spec.Instructions,
		Metadata:     maps.Clone(spec.ProfileMetadata),
	})
}

func systemManagedAgentSpecFor(agentID string) (systemManagedAgentSpec, bool) {
	agentID = normalizeAgentProfileID(agentID)
	if agentID == "" {
		return systemManagedAgentSpec{}, false
	}
	spec, ok := systemManagedAgentRegistrySnapshot().byID[agentID]
	return spec, ok
}

func isSystemManagedAgentProfileID(profileID string) bool {
	profileID = normalizeAgentProfileID(profileID)
	if profileID == "" {
		return false
	}
	_, ok := systemManagedAgentRegistrySnapshot().byID[profileID]
	return ok
}

func defaultSystemManagedAgentBinding(spec systemManagedAgentSpec) agentprofile.Binding {
	return normalizeSystemManagedAgentBinding(spec, defaultAgentProfileBinding(spec.ID))
}

// normalizeSystemManagedAgentBinding reads only the registry-owned immutable
// spec identity and binding policy; profile/runtime fields stay out of this path.
func normalizeSystemManagedAgentBinding(spec systemManagedAgentSpec, binding agentprofile.Binding) agentprofile.Binding {
	agentID := normalizeAgentProfileID(spec.ID)
	policy := spec.BindingPolicy
	binding.ProfileID = agentID
	binding = agentprofile.NormalizeBinding(binding)
	binding.ProfileID = agentID
	if policy.ForceEnabled {
		binding.Enabled = boolPtr(true)
	}
	binding.Status = agentprofile.BindingStatusOK
	binding.Warning = ""
	if binding.Target == agentprofile.BindingTargetACP && !policy.AllowExternalACP {
		binding.Target = agentprofile.BindingTargetSelf
		binding.Model = ""
		binding.ACPAgent = ""
		binding.ACPModel = ""
		binding.ReasoningEffort = ""
	}
	return binding
}

func validateSystemManagedAgentBinding(spec systemManagedAgentSpec, binding agentprofile.Binding) error {
	agentID := normalizeAgentProfileID(spec.ID)
	policy := spec.BindingPolicy
	binding = agentprofile.NormalizeBinding(binding)
	if binding.Target == agentprofile.BindingTargetACP && !policy.AllowExternalACP {
		return fmt.Errorf("gatewayapp: %s cannot bind to an external ACP agent", agentID)
	}
	return nil
}

func guardianSystemManagedAgentSpec() systemManagedAgentSpec {
	return systemManagedAgentSpec{
		ID:           guardianProfileID,
		Name:         "Guardian",
		Description:  "Reviews approval requests for auto-review mode.",
		Instructions: guardianPolicyPrompt(),
		ProfileMetadata: map[string]any{
			"source":         "caelis",
			"built_in":       true,
			"system_managed": true,
		},
		SessionSuffix:     "approval-review",
		SessionID:         guardianReviewSessionIDFromMetadata,
		Purpose:           systemManagedAgentPurposeApprovalReview,
		CapabilityProfile: systemManagedAgentCapabilityNone,
		BindingPolicy: systemManagedAgentBindingPolicy{
			ForceEnabled:     true,
			AllowExternalACP: false,
		},
		ReasoningEffort: "none",
		SessionMetadata: map[string]any{
			"guardian": true,
			"source":   "auto-review",
		},
	}
}

func guardianReviewSessionIDFromMetadata(parent session.Session, metadata map[string]any) string {
	return guardianReviewSessionID(parent, stringFromMap(metadata, systemManagedAgentStateReuseKey))
}

func systemManagedAgentSessionForParent(parent session.Session, spec systemManagedAgentSpec, metadata map[string]any) session.Session {
	out := session.CloneSession(parent)
	if strings.EqualFold(strings.TrimSpace(stringFromMap(out.Metadata, "system_managed_agent")), strings.TrimSpace(spec.ID)) {
		out.Participants = nil
		return out
	}
	if spec.SessionID != nil {
		out.SessionID = strings.TrimSpace(spec.SessionID(parent, metadata))
	}
	suffix := firstNonEmpty(strings.TrimSpace(spec.SessionSuffix), strings.TrimSpace(spec.ID))
	out.SessionID = firstNonEmpty(out.SessionID, strings.TrimSpace(parent.SessionID)+"-"+suffix, suffix)
	out.Metadata = maps.Clone(spec.SessionMetadata)
	if out.Metadata == nil {
		out.Metadata = map[string]any{}
	}
	out.Metadata["system_managed_agent"] = strings.TrimSpace(spec.ID)
	if purpose := stringFromMap(metadata, "system_managed_purpose"); purpose != "" {
		out.Metadata["system_managed_purpose"] = purpose
	}
	out.Participants = nil
	return out
}
