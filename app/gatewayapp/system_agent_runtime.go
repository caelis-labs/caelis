package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
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
	systemManagedAgentPurposeMemorySteward  systemManagedAgentPurpose = "memory_steward"
)

type systemManagedAgentCapabilityProfile string

const (
	systemManagedAgentCapabilityNone systemManagedAgentCapabilityProfile = "none"
	// ReadOnly accepts owner-supplied evidence tools. Their sandbox enforces
	// temporary-only writes; no main-agent tool registry is inherited.
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
	AgentID            string
	Purpose            systemManagedAgentPurpose
	Model              model.LLM
	ParentSession      session.Session
	Input              string
	PolicyInstructions string
	InputUserEvidence  []string
	Events             []*session.Event
	Tools              []tool.Tool
	Output             *model.OutputSpec
	Compaction         sdkruntime.CompactionConfig
	Metadata           map[string]any
	CapabilityProfile  systemManagedAgentCapabilityProfile
}

type systemManagedAgentRunResult struct {
	RuntimeReused  bool
	PrepareMS      int64
	AssistantEvent *session.Event
	ContextEvents  []*session.Event
	Text           string
}

// systemManagedAgentRunPlan is the normalized runtime plan after applying the
// agent spec defaults, purpose, capability profile, session projection, and
// metadata used by the underlying Agent runtime.
type systemManagedAgentRunPlan struct {
	Spec               systemManagedAgentSpec
	AgentID            string
	Purpose            systemManagedAgentPurpose
	CapabilityProfile  systemManagedAgentCapabilityProfile
	Model              model.LLM
	Session            session.Session
	Input              string
	PolicyInstructions string
	InputUserEvidence  []string
	Events             []*session.Event
	Tools              []tool.Tool
	Output             *model.OutputSpec
	Compaction         sdkruntime.CompactionConfig
	Metadata           map[string]any
}

type systemManagedAgentRunner interface {
	Run(context.Context, systemManagedAgentRunRequest) (systemManagedAgentRunResult, error)
}

func boolPtr(value bool) *bool {
	return &value
}

type systemManagedAgentRuntime struct {
	config systemManagedAgentRuntimeConfig
	// A resident execution is exclusively leased by its domain owner. It is
	// replaced only when the supplied checkpoint no longer extends its history.
	resident  bool
	execution *systemManagedAgentExecution
}

type systemManagedAgentExecution struct {
	staging session.Service
	core    *sdkruntime.Runtime
	history []*session.Event
	key     string
}

func systemManagedExecutionKey(plan systemManagedAgentRunPlan) string {
	definitions := make([]tool.Definition, 0, len(plan.Tools))
	for _, t := range plan.Tools {
		definitions = append(definitions, t.Definition())
	}
	key, _ := json.Marshal([]any{plan.Session.SessionID, plan.Model.Name(), plan.PolicyInstructions, definitions, plan.Output, plan.Compaction})
	return string(key)
}

func systemManagedHistoryExtends(previous, next []*session.Event) bool {
	if len(previous) > len(next) {
		return false
	}
	for i, event := range previous {
		if event == nil || next[i] == nil || session.EventTypeOf(event) != session.EventTypeOf(next[i]) || !reflect.DeepEqual(event.Message, next[i].Message) || session.EventText(event) != session.EventText(next[i]) {
			return false
		}
	}
	return true
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
	started := time.Now()
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
	instructions := strings.TrimSpace(plan.Spec.Instructions)
	if policy := strings.TrimSpace(plan.PolicyInstructions); policy != "" {
		instructions += "\n\n" + policy
	}
	metadata := chat.Metadata(instructions)
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
	var previous *systemManagedAgentExecution
	key := systemManagedExecutionKey(plan)
	if r != nil && r.resident && r.execution != nil && r.execution.key == key && systemManagedHistoryExtends(r.execution.history, plan.Events) {
		previous = r.execution
	}
	var staging session.Service
	seed := plan.Events
	if previous != nil {
		staging = previous.staging
		seed = seed[len(previous.history):]
	} else {
		staging = config.StagingSessions()
	}
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
	if len(seed) > 0 {
		batch, ok := staging.(session.EventBatchService)
		if !ok {
			return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: system-managed agent staging service requires event batches")
		}
		if _, err := batch.AppendEvents(stagingCtx, session.AppendEventsRequest{SessionRef: activeSession.SessionRef, Events: session.CloneEvents(seed)}); err != nil {
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
	var compactor compact.Engine
	if plan.Purpose == systemManagedAgentPurposeApprovalReview {
		guardian := guardianTurnCompactor{output: req.Output}
		for _, t := range req.Tools {
			if query, ok := t.(guardianQueryTool); ok {
				guardian.evidence = query.owner.evidenceStore()
				guardian.queries = query.owner
				break
			}
		}
		compactor = guardian
	}
	var core *sdkruntime.Runtime
	if previous != nil {
		core = previous.core
	} else {
		core, err = sdkruntime.New(sdkruntime.Config{
			Compactor:             compactor,
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
	prepareMS := guardianElapsed(started)
	result, err := collectSystemManagedAgentResult(stagingCtx, staging, activeSession.SessionRef, baselineSeq, run.Handle)
	result.RuntimeReused, result.PrepareMS = previous != nil, prepareMS
	if r != nil && r.resident {
		if err == nil {
			r.execution = &systemManagedAgentExecution{staging: staging, core: core, history: session.CloneEvents(result.ContextEvents), key: key}
		} else {
			r.execution = nil
		}
	}
	return result, err
}

// collectSystemManagedAgentResult waits for producer quiescence, then reads the
// isolated staging Session as the sole authority for the final assistant result.
func collectSystemManagedAgentResult(
	ctx context.Context,
	staging session.Service,
	ref session.SessionRef,
	baselineSeq uint64,
	handle agent.Runner,
) (systemManagedAgentRunResult, error) {
	result := systemManagedAgentRunResult{}
	if handle != nil {
		// The run already receives caller cancellation. As with the owning
		// Gateway Turn, the wait must still prove producer quiescence before
		// observers are consumed or the resident execution lane can be reused.
		// A non-cooperative producer keeps this invocation draining; Close alone
		// or a timed-out wait cannot establish completion.
		if err := handle.WaitCompletion(context.WithoutCancel(ctx)); err != nil || ctx.Err() != nil {
			return result, errors.Join(ctx.Err(), err)
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
		Spec:               spec,
		AgentID:            strings.TrimSpace(spec.ID),
		Purpose:            purpose,
		CapabilityProfile:  capabilityProfile,
		Model:              req.Model,
		Session:            systemManagedAgentSessionForParent(req.ParentSession, spec, sessionMetadata),
		Input:              strings.TrimSpace(req.Input),
		PolicyInstructions: strings.TrimSpace(req.PolicyInstructions),
		InputUserEvidence:  append([]string(nil), req.InputUserEvidence...),
		Events:             session.CloneEvents(req.Events),
		Tools:              tools,
		Output:             req.Output,
		Compaction:         req.Compaction,
		Metadata:           metadata,
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
			stewardSystemManagedAgentSpec(),
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
