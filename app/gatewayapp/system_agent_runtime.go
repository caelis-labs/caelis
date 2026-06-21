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

type systemManagedAgentSpec struct {
	ID              string
	Name            string
	Description     string
	Capabilities    []string
	Instructions    string
	ProfileMetadata map[string]any
	SessionID       func(session.Session, map[string]any) string
	SessionSuffix   string
	SessionMetadata map[string]any
	ReasoningEffort string
	Tools           []tool.Tool
}

type systemManagedAgentRunRequest struct {
	AgentID       string
	Model         model.LLM
	ParentSession session.Session
	Events        []*session.Event
	Tools         []tool.Tool
	Output        *model.OutputSpec
	Metadata      map[string]any
}

type systemManagedAgentRunResult struct {
	Events         []*session.Event
	AssistantEvent *session.Event
	Text           string
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
	spec, ok := systemManagedAgentSpecFor(req.AgentID)
	if !ok {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: unknown system-managed agent %q", strings.TrimSpace(req.AgentID))
	}
	if req.Model == nil {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: system-managed agent %q requires a model", spec.ID)
	}
	factory := agent.AgentFactory(nil)
	if r != nil {
		factory = r.factory
	}
	if factory == nil {
		factory = chat.Factory{}
	}
	metadata := chat.Metadata(spec.Instructions)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["system_managed_agent"] = spec.ID
	if strings.TrimSpace(spec.ReasoningEffort) != "" {
		metadata["reasoning_effort"] = strings.TrimSpace(spec.ReasoningEffort)
	}
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	tools := append([]tool.Tool(nil), spec.Tools...)
	tools = append(tools, req.Tools...)
	runtimeAgent, err := factory.NewAgent(ctx, agent.AgentSpec{
		Name:  spec.ID,
		Model: req.Model,
		Tools: tools,
		Request: agent.ModelRequestOptions{
			Stream: boolPtr(false),
			Output: req.Output,
		},
		Metadata: metadata,
	})
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context: ctx,
		Session: systemManagedAgentSessionForParent(req.ParentSession, spec, req.Metadata),
		Events:  req.Events,
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
		SessionSuffix:   "approval-review",
		SessionID:       guardianReviewSessionIDFromMetadata,
		ReasoningEffort: "none",
		SessionMetadata: map[string]any{
			"guardian": true,
			"source":   "auto-review",
		},
	}
}

func guardianReviewSessionIDFromMetadata(parent session.Session, metadata map[string]any) string {
	return guardianReviewSessionID(parent, stringFromMap(metadata, guardianStateReuseKey))
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
	out.Participants = nil
	return out
}
