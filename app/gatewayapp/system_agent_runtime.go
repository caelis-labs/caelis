package gatewayapp

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	controlagents "github.com/caelis-labs/caelis/control/agents"
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

// systemManagedAgentSpec describes one built-in system-owned scene and
// its default runtime cuts. Model and reasoning effort belong exclusively to
// Control fixed-handle placement, not this scene metadata. The spec remains
// app-private until another layer needs a stable public system-agent contract.
type systemManagedAgentSpec struct {
	ID                string
	Instructions      string
	SessionID         func(session.Session, map[string]any) string
	SessionSuffix     string
	SessionMetadata   map[string]any
	Purpose           systemManagedAgentPurpose
	CapabilityProfile systemManagedAgentCapabilityProfile
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
	Input             string
	InputUserEvidence []string
	Events            []*session.Event
	Tools             []tool.Tool
	Output            *model.OutputSpec
	Compaction        sdkruntime.CompactionConfig
	Metadata          map[string]any
	CapabilityProfile systemManagedAgentCapabilityProfile
}

type systemManagedAgentRunResult struct {
	AssistantEvent *session.Event
	ContextEvents  []*session.Event
	Text           string
}

// systemManagedAgentCompactRequest asks the transient system-agent runner to
// compact only its supplied process-local context. The staging Session used to
// perform the compaction is discarded with the call.
type systemManagedAgentCompactRequest struct {
	AgentID       string
	Purpose       systemManagedAgentPurpose
	Model         model.LLM
	ParentSession session.Session
	Events        []*session.Event
	Compaction    sdkruntime.CompactionConfig
}

type systemManagedAgentCompactResult struct {
	Events    []*session.Event
	Compacted bool
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
	Input             string
	InputUserEvidence []string
	Events            []*session.Event
	Tools             []tool.Tool
	Output            *model.OutputSpec
	Compaction        sdkruntime.CompactionConfig
	Metadata          map[string]any
}

type systemManagedAgentRunner interface {
	Run(context.Context, systemManagedAgentRunRequest) (systemManagedAgentRunResult, error)
}

type systemManagedAgentContextCompactor interface {
	CompactContext(context.Context, systemManagedAgentCompactRequest) (systemManagedAgentCompactResult, error)
}

func boolPtr(value bool) *bool {
	return &value
}

type systemManagedAgentRuntime struct {
	config systemManagedAgentRuntimeConfig
}

type systemManagedAgentRuntimeConfig struct {
	AgentFactory          agent.AgentFactory
	StagingSessions       func() session.Service
	LifecycleInterceptors []agent.LifecycleInterceptor
	TraceSink             agent.TraceSink
	Guardrails            []agent.GuardrailSpec
	Diagnostics           *slog.Logger
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
	return newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{AgentFactory: factory})
}

func newSystemManagedAgentRuntimeWithConfig(config systemManagedAgentRuntimeConfig) *systemManagedAgentRuntime {
	if config.AgentFactory == nil {
		config.AgentFactory = chat.Factory{}
	}
	if config.StagingSessions == nil {
		config.StagingSessions = func() session.Service {
			return inmemory.NewStore(inmemory.Config{})
		}
	}
	config.LifecycleInterceptors = append([]agent.LifecycleInterceptor(nil), config.LifecycleInterceptors...)
	config.Guardrails = append([]agent.GuardrailSpec(nil), config.Guardrails...)
	return &systemManagedAgentRuntime{config: config}
}

func (r *systemManagedAgentRuntime) Run(ctx context.Context, req systemManagedAgentRunRequest) (systemManagedAgentRunResult, error) {
	plan, err := systemManagedAgentRunPlanFor(req)
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	config := systemManagedAgentRuntimeConfig{}
	if r != nil {
		config = r.config
	}
	if config.AgentFactory == nil || config.StagingSessions == nil {
		config = newSystemManagedAgentRuntimeWithConfig(config).config
	}
	metadata := chat.Metadata(plan.Spec.Instructions)
	if metadata == nil {
		metadata = map[string]any{}
	}
	for key, value := range plan.Metadata {
		metadata[key] = value
	}
	// System-agent attempts execute through Core Runtime in an isolated staging
	// session. The domain owner validates the result before atomically advancing
	// its process-local conversation, so a malformed attempt receives the common
	// safety and journal pipeline without poisoning the next model prefix.
	staging := config.StagingSessions()
	if staging == nil {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: system-managed agent staging session service is unavailable")
	}
	// A system-managed attempt is a distinct Runtime placement scope. The
	// caller may be executing inside the parent Session's fenced Turn; carrying
	// that fence into the isolated staging Session makes every Runtime mutation
	// fail closed against the wrong fence.
	stagingCtx := session.ContextWithoutRuntimeFence(ctx)
	activeSession, err := startSystemManagedAgentStagingSession(stagingCtx, staging, plan.Session)
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	if len(plan.Events) > 0 {
		batch, ok := staging.(session.EventBatchService)
		if !ok {
			return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: system-managed agent staging service requires event batches")
		}
		if _, err := batch.AppendEvents(stagingCtx, session.AppendEventsRequest{SessionRef: activeSession.SessionRef, Events: session.CloneEvents(plan.Events)}); err != nil {
			return systemManagedAgentRunResult{}, err
		}
	}
	baselineEvents, err := staging.Events(stagingCtx, session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	var baselineSeq uint64
	for _, event := range baselineEvents {
		if event != nil && event.Seq > baselineSeq {
			baselineSeq = event.Seq
		}
	}
	core, err := sdkruntime.New(sdkruntime.Config{
		Sessions:              staging,
		AgentFactory:          config.AgentFactory,
		Compaction:            plan.Compaction,
		LifecycleInterceptors: config.LifecycleInterceptors,
		TraceSink:             config.TraceSink,
		Guardrails:            config.Guardrails,
		Diagnostics:           config.Diagnostics,
	})
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	run, err := core.Run(stagingCtx, agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      strings.TrimSpace(plan.Input),
		InputActor: session.ActorRef{Kind: session.ActorKindSystem, Name: plan.AgentID},
		InputCompaction: &session.EventCompactionContext{
			UserEvidence: append([]string(nil), plan.InputUserEvidence...),
		},
		AgentSpec: agent.AgentSpec{
			Name:  plan.AgentID,
			Model: plan.Model,
			Tools: plan.Tools,
			Request: agent.ModelRequestOptions{
				// Approval/Guardian and other system-managed reviews must stream.
				// Anthropic-compatible SDKs reject long non-streaming requests, and
				// there is no product path that needs a non-stream Guardian call.
				Stream: boolPtr(true),
				Output: plan.Output,
			},
			Metadata: metadata,
		},
	})
	if err != nil {
		return systemManagedAgentRunResult{}, err
	}
	if run.Handle == nil {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: system-managed agent runtime returned no handle")
	}
	defer run.Handle.Close()
	return collectSystemManagedAgentResult(stagingCtx, staging, activeSession.SessionRef, baselineSeq, run.Handle)
}

// CompactContext performs one explicit compaction of a system agent's supplied
// in-memory dialogue. It does not create or update any durable system-agent
// Session; the returned Events are the only state that can leave the staging
// store.
func (r *systemManagedAgentRuntime) CompactContext(
	ctx context.Context,
	req systemManagedAgentCompactRequest,
) (systemManagedAgentCompactResult, error) {
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID:       req.AgentID,
		Purpose:       req.Purpose,
		Model:         req.Model,
		ParentSession: req.ParentSession,
		Events:        req.Events,
		Compaction:    req.Compaction,
	})
	if err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	if len(plan.Events) == 0 {
		return systemManagedAgentCompactResult{}, nil
	}

	config := systemManagedAgentRuntimeConfig{}
	if r != nil {
		config = r.config
	}
	if config.AgentFactory == nil || config.StagingSessions == nil {
		config = newSystemManagedAgentRuntimeWithConfig(config).config
	}
	staging := config.StagingSessions()
	if staging == nil {
		return systemManagedAgentCompactResult{}, fmt.Errorf("gatewayapp: system-managed agent staging session service is unavailable")
	}
	stagingCtx := session.ContextWithoutRuntimeFence(ctx)
	activeSession, err := startSystemManagedAgentStagingSession(stagingCtx, staging, plan.Session)
	if err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	batch, ok := staging.(session.EventBatchService)
	if !ok {
		return systemManagedAgentCompactResult{}, fmt.Errorf("gatewayapp: system-managed agent staging service requires event batches")
	}
	if _, err := batch.AppendEvents(stagingCtx, session.AppendEventsRequest{
		SessionRef: activeSession.SessionRef,
		Events:     session.CloneEvents(plan.Events),
	}); err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	core, err := sdkruntime.New(sdkruntime.Config{
		Sessions:              staging,
		AgentFactory:          config.AgentFactory,
		Compaction:            plan.Compaction,
		LifecycleInterceptors: config.LifecycleInterceptors,
		TraceSink:             config.TraceSink,
		Guardrails:            config.Guardrails,
		Diagnostics:           config.Diagnostics,
	})
	if err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	compacted, err := core.Compact(stagingCtx, sdkruntime.CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      plan.Model,
		Trigger:    "guardian_prompt_budget",
	})
	if err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	events, err := staging.Events(stagingCtx, session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		return systemManagedAgentCompactResult{}, err
	}
	return systemManagedAgentCompactResult{
		Events:    systemManagedAgentConversationEvents(events),
		Compacted: compacted.Compacted,
	}, nil
}

// collectSystemManagedAgentResult treats Runner events as a best-effort live
// observation stream. The isolated staging Session is the authoritative source
// for the final assistant result after the execution producer is quiescent.
func collectSystemManagedAgentResult(
	ctx context.Context,
	staging session.Service,
	ref session.SessionRef,
	baselineSeq uint64,
	handle agent.Runner,
) (systemManagedAgentRunResult, error) {
	result := systemManagedAgentRunResult{}
	for event, runErr := range handle.Events() {
		if runErr != nil {
			if _, ok := agent.AsEventStreamGap(runErr); ok {
				continue
			}
			return result, runErr
		}
		if event == nil {
			continue
		}
		cloned := session.CloneEvent(event)
		if session.EventTypeOf(cloned) == session.EventTypeAssistant {
			result.AssistantEvent = cloned
		}
	}
	durableEvents, err := staging.Events(ctx, session.EventsRequest{
		SessionRef:       ref,
		IncludeTransient: true,
	})
	if err != nil {
		return result, err
	}
	for _, event := range durableEvents {
		if event == nil || event.Seq <= baselineSeq || !session.IsCanonicalHistoryEvent(event) {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			result.AssistantEvent = session.CloneEvent(event)
		}
	}
	result.ContextEvents = systemManagedAgentConversationEvents(durableEvents)
	if result.AssistantEvent != nil {
		result.Text = session.EventText(result.AssistantEvent)
	}
	return result, nil
}

// systemManagedAgentConversationEvents returns a reusable, process-local model
// context rather than a durable Session projection. Compact coverage sequence
// numbers belong to the isolated staging Session, so the retained checkpoint is
// deliberately normalized to a legacy in-memory checkpoint before a later
// invocation assigns fresh staging sequence numbers.
func systemManagedAgentConversationEvents(events []*session.Event) []*session.Event {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	out := make([]*session.Event, 0, len(promptEvents))
	for _, event := range promptEvents {
		if event == nil {
			continue
		}
		cloned := session.CloneEvent(event)
		cloned.ID = ""
		cloned.IdempotencyKey = ""
		cloned.SessionID = ""
		cloned.Seq = 0
		cloned.Time = time.Time{}
		cloned.Scope = nil
		if compact.IsCompactEvent(cloned) && cloned.Meta != nil {
			delete(cloned.Meta, compact.MetaKeyCompact)
			if len(cloned.Meta) == 0 {
				cloned.Meta = nil
			}
		}
		out = append(out, cloned)
	}
	return out
}

func startSystemManagedAgentStagingSession(ctx context.Context, service session.Service, planned session.Session) (session.Session, error) {
	ref := session.NormalizeSessionRef(planned.SessionRef)
	if ref.AppName == "" {
		ref.AppName = "caelis-system"
	}
	if ref.UserID == "" {
		ref.UserID = "system"
	}
	return service.StartSession(ctx, session.StartSessionRequest{
		AppName: ref.AppName,
		UserID:  ref.UserID,
		Workspace: session.WorkspaceRef{
			Key: ref.WorkspaceKey,
			CWD: strings.TrimSpace(planned.CWD),
		},
		PreferredSessionID: ref.SessionID,
		Title:              planned.Title,
		Metadata:           session.CloneState(planned.Metadata),
	})
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
		Input:             strings.TrimSpace(req.Input),
		InputUserEvidence: append([]string(nil), req.InputUserEvidence...),
		Events:            session.CloneEvents(req.Events),
		Tools:             tools,
		Output:            req.Output,
		Compaction:        req.Compaction,
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
	spec.ID = controlagents.NormalizeName(spec.ID)
	if !controlagents.IsName(spec.ID) {
		spec.ID = ""
	}
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

func systemManagedAgentSpecFor(agentID string) (systemManagedAgentSpec, bool) {
	agentID = controlagents.NormalizeName(agentID)
	if agentID == "" {
		return systemManagedAgentSpec{}, false
	}
	spec, ok := systemManagedAgentRegistrySnapshot().byID[agentID]
	return spec, ok
}

func guardianSystemManagedAgentSpec() systemManagedAgentSpec {
	return systemManagedAgentSpec{
		ID:                guardianSceneID,
		Instructions:      guardianPolicyPrompt(),
		SessionSuffix:     "approval-review",
		Purpose:           systemManagedAgentPurposeApprovalReview,
		CapabilityProfile: systemManagedAgentCapabilityNone,
		SessionMetadata: map[string]any{
			"guardian": true,
			"source":   "auto-review",
		},
	}
}

func systemManagedAgentSessionForParent(parent session.Session, spec systemManagedAgentSpec, metadata map[string]any) session.Session {
	out := session.CloneSession(parent)
	if strings.EqualFold(strings.TrimSpace(stringFromMap(out.Metadata, "system_managed_agent")), strings.TrimSpace(spec.ID)) {
		out.Participants = nil
		return out
	}
	if spec.SessionID != nil {
		out.SessionID = strings.TrimSpace(spec.SessionID(parent, metadata))
	} else {
		out.SessionID = ""
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
