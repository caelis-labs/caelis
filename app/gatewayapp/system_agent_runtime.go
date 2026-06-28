package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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
	return agentprofile.NormalizeProfile(agentprofile.Profile{
		ID:           spec.ID,
		Name:         spec.Name,
		Description:  spec.Description,
		Capabilities: append([]string(nil), spec.Capabilities...),
		Instructions: spec.Instructions,
		Metadata:     maps.Clone(spec.ProfileMetadata),
	}), true
}

func systemManagedAgentSpecFor(agentID string) (systemManagedAgentSpec, bool) {
	switch normalizeAgentProfileID(agentID) {
	case guardianProfileID:
		return guardianSystemManagedAgentSpec(), true
	default:
		return systemManagedAgentSpec{}, false
	}
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
		ReasoningEffort:   "none",
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
