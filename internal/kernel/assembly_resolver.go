package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

const (
	// StateCurrentModelAlias is the durable session-state key for a per-session
	// model reference selected by the TUI. Newer clients store stable model IDs
	// here; older session state may still contain visible model aliases.
	StateCurrentModelAlias = "gateway.current_model_alias"
	// StateCurrentApprovalMode is the durable session-state key for a
	// per-session approval routing override selected by the TUI.
	StateCurrentApprovalMode = "gateway.current_approval_mode"
	// StateCurrentPolicyProfile is the durable session-state key for a
	// per-session policy profile override.
	StateCurrentPolicyProfile = "gateway.current_policy_profile"
	// StateCurrentReasoningEffort is the durable session-state key for a
	// per-session reasoning effort override selected by the TUI.
	StateCurrentReasoningEffort = "gateway.current_reasoning_effort"
	// StateUsageAccounting is the durable non-invocation session-state key for
	// token usage bookkeeping that must not enter canonical prompt history.
	StateUsageAccounting = "gateway.usage.v1"
)

var unsupportedLegacyStateKeys = []string{
	"gateway.current_session_mode",
	"gateway.current_sandbox_mode",
}

type ModelResolution struct {
	Model model.LLM
	// ProfileID is the Control-owned selectable identity that produced Model.
	// The current main Session is provider-backed; hosts must supply its
	// provider ModelProfile rather than making tool assembly derive it again.
	ProfileID              string
	ReasoningEffort        string
	DefaultReasoningEffort string
}

type ModelLookup interface {
	ResolveModel(context.Context, string, int) (ModelResolution, error)
}

type AssemblyResolverConfig struct {
	Sessions interface {
		SnapshotState(context.Context, session.SessionRef) (map[string]any, error)
	}
	Assembly          assembly.ResolvedAssembly
	DefaultModelAlias string
	ContextWindow     int
	ModelLookup       ModelLookup
	Tools             []tool.Tool
	AgentName         string
	BaseMetadata      map[string]any
	ToolAugmenter     ToolAugmenter
	// ApprovalModelResolver optionally returns the fully configured model for
	// the Control-managed approval reviewer. handled=false preserves the current
	// Session model behavior.
	ApprovalModelResolver func(context.Context, session.SessionRef) (resolved model.LLM, handled bool, err error)
}

type AssemblyResolver struct {
	mu sync.RWMutex

	sessions interface {
		SnapshotState(context.Context, session.SessionRef) (map[string]any, error)
	}
	assembly              assembly.ResolvedAssembly
	defaultModelAlias     string
	contextWindow         int
	modelLookup           ModelLookup
	tools                 []tool.Tool
	agentName             string
	baseMetadata          map[string]any
	toolAugmenter         ToolAugmenter
	approvalModelResolver func(context.Context, session.SessionRef) (model.LLM, bool, error)
}

type ToolAugmenter func(context.Context, ToolAugmentContext) (ToolAugmentation, error)

type ToolAugmentContext struct {
	SessionRef session.SessionRef
	State      map[string]any
	Session    controlplacement.SessionContext
}

type ToolAugmentation struct {
	Tools    []tool.Tool
	Metadata map[string]any
}

type modelAliasLister interface {
	ListModelAliases() []string
}

type modelAliasValidator interface {
	HasAlias(string) bool
}

func NewAssemblyResolver(cfg AssemblyResolverConfig) (*AssemblyResolver, error) {
	if cfg.ModelLookup == nil {
		return nil, fmt.Errorf("gateway: model lookup is required")
	}
	agentName := strings.TrimSpace(cfg.AgentName)
	if agentName == "" {
		agentName = "main"
	}
	return &AssemblyResolver{
		sessions:              cfg.Sessions,
		assembly:              assembly.CloneResolvedAssembly(cfg.Assembly),
		defaultModelAlias:     strings.TrimSpace(cfg.DefaultModelAlias),
		contextWindow:         cfg.ContextWindow,
		modelLookup:           cfg.ModelLookup,
		tools:                 append([]tool.Tool(nil), cfg.Tools...),
		agentName:             agentName,
		baseMetadata:          cloneMap(cfg.BaseMetadata),
		toolAugmenter:         cfg.ToolAugmenter,
		approvalModelResolver: cfg.ApprovalModelResolver,
	}, nil
}

// SetModelLookup replaces the model lookup used by ResolveTurn. This supports
// runtime model reconfiguration (e.g. /connect).
func (r *AssemblyResolver) SetModelLookup(lookup ModelLookup, defaultAlias string) {
	if r == nil || lookup == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelLookup = lookup
	r.defaultModelAlias = strings.TrimSpace(defaultAlias)
}

func (r *AssemblyResolver) ResolveTurn(ctx context.Context, intent TurnIntent) (ResolvedTurn, error) {
	state, err := r.snapshotState(ctx, intent.SessionRef)
	if err != nil {
		return ResolvedTurn{}, err
	}
	if key := unsupportedLegacyStateKey(state); key != "" {
		return ResolvedTurn{}, fmt.Errorf("gateway: %w: session state contains legacy key %q", session.ErrUnsupportedLegacyFormat, key)
	}
	snap := r.snapshot()
	if snap.modelLookup == nil {
		return ResolvedTurn{}, fmt.Errorf("gateway: model lookup is required")
	}
	alias := resolveModelAliasWith(snap.modelLookup, snap.defaultModelAlias, state, intent.ModelHint)
	modelResolution, err := snap.modelLookup.ResolveModel(ctx, alias, snap.contextWindow)
	if err != nil {
		return ResolvedTurn{}, err
	}
	spec, err := resolveAgentSpecWith(ctx, snap, intent, state, modelResolution)
	if err != nil {
		return ResolvedTurn{}, err
	}

	return ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef:   intent.SessionRef,
			Input:        intent.Input,
			ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
			AgentSpec:    spec,
		},
	}, nil
}

func (r *AssemblyResolver) ResolveControllerTurn(ctx context.Context, intent TurnIntent) (ResolvedTurn, error) {
	state, err := r.snapshotState(ctx, intent.SessionRef)
	if err != nil {
		return ResolvedTurn{}, err
	}
	if key := unsupportedLegacyStateKey(state); key != "" {
		return ResolvedTurn{}, fmt.Errorf("gateway: %w: session state contains legacy key %q", session.ErrUnsupportedLegacyFormat, key)
	}
	spec, err := resolveAgentSpecWith(ctx, r.snapshot(), intent, state, ModelResolution{})
	if err != nil {
		return ResolvedTurn{}, err
	}
	return ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef:   intent.SessionRef,
			Input:        intent.Input,
			ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
			AgentSpec:    spec,
		},
	}, nil
}

// ResolveApprovalModel resolves the configured approval-review model, falling
// back to the model currently selected for the Session.
func (r *AssemblyResolver) ResolveApprovalModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if r == nil {
		return nil, fmt.Errorf("gateway: model lookup is required")
	}
	snap := r.snapshot()
	if snap.modelLookup == nil {
		return nil, fmt.Errorf("gateway: model lookup is required")
	}
	if snap.approvalModelResolver != nil {
		resolved, handled, err := snap.approvalModelResolver(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("gateway: resolve approval model: %w", err)
		}
		if handled {
			if resolved == nil {
				return nil, fmt.Errorf("gateway: approval model resolver returned no model")
			}
			return resolved, nil
		}
	}
	state, err := r.snapshotState(ctx, ref)
	if err != nil {
		return nil, err
	}
	model, err := snap.modelLookup.ResolveModel(ctx, resolveModelAliasWith(snap.modelLookup, snap.defaultModelAlias, state, ""), snap.contextWindow)
	if err != nil {
		return nil, err
	}
	return model.Model, nil
}

func (r *AssemblyResolver) resolveModelAlias(state map[string]any, hint string) string {
	if r == nil {
		return strings.TrimSpace(hint)
	}
	snap := r.snapshot()
	return resolveModelAliasWith(snap.modelLookup, snap.defaultModelAlias, state, hint)
}

func resolveModelAliasWith(lookup ModelLookup, defaultAlias string, state map[string]any, hint string) string {
	alias := strings.TrimSpace(hint)
	if alias == "" {
		alias = CurrentModelAlias(state)
		if validator, ok := lookup.(modelAliasValidator); ok && alias != "" && !validator.HasAlias(alias) {
			alias = ""
		}
	}
	if alias == "" {
		alias = defaultAlias
	}
	return alias
}

// ListModelAliases returns the known model aliases relevant to one session.
// Session-local overrides are returned first, followed by resolver-known
// aliases and the resolver default alias.
func (r *AssemblyResolver) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	state, err := r.snapshotState(ctx, ref)
	if err != nil {
		return nil, err
	}
	snap := r.snapshot()
	aliases := make([]string, 0, 4)
	if alias := CurrentModelAlias(state); alias != "" {
		if validator, ok := snap.modelLookup.(modelAliasValidator); !ok || validator.HasAlias(alias) {
			aliases = append(aliases, alias)
		}
	}
	if lister, ok := snap.modelLookup.(modelAliasLister); ok {
		aliases = append(aliases, lister.ListModelAliases()...)
	}
	if snap.defaultModelAlias != "" {
		aliases = append(aliases, snap.defaultModelAlias)
	}
	return dedupeOrderedStrings(aliases), nil
}

type assemblyResolverSnapshot struct {
	assembly              assembly.ResolvedAssembly
	defaultModelAlias     string
	contextWindow         int
	modelLookup           ModelLookup
	tools                 []tool.Tool
	agentName             string
	baseMetadata          map[string]any
	toolAugmenter         ToolAugmenter
	approvalModelResolver func(context.Context, session.SessionRef) (model.LLM, bool, error)
}

func (r *AssemblyResolver) snapshot() assemblyResolverSnapshot {
	if r == nil {
		return assemblyResolverSnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return assemblyResolverSnapshot{
		assembly:              assembly.CloneResolvedAssembly(r.assembly),
		defaultModelAlias:     r.defaultModelAlias,
		contextWindow:         r.contextWindow,
		modelLookup:           r.modelLookup,
		tools:                 append([]tool.Tool(nil), r.tools...),
		agentName:             r.agentName,
		baseMetadata:          cloneMap(r.baseMetadata),
		toolAugmenter:         r.toolAugmenter,
		approvalModelResolver: r.approvalModelResolver,
	}
}

func (r *AssemblyResolver) snapshotState(ctx context.Context, ref session.SessionRef) (map[string]any, error) {
	if r == nil || r.sessions == nil || strings.TrimSpace(ref.SessionID) == "" {
		return map[string]any{}, nil
	}
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return nil, wrapSessionError(err)
	}
	return state, nil
}

func (r *AssemblyResolver) resolveMetadata(intent TurnIntent, state map[string]any, model ModelResolution) (map[string]any, error) {
	snap := r.snapshot()
	return resolveMetadataWith(snap.baseMetadata, snap.assembly, intent, state, model)
}

func resolveAgentSpecWith(ctx context.Context, snap assemblyResolverSnapshot, intent TurnIntent, state map[string]any, modelResolution ModelResolution) (agent.AgentSpec, error) {
	metadata, err := resolveMetadataWith(snap.baseMetadata, snap.assembly, intent, state, modelResolution)
	if err != nil {
		return agent.AgentSpec{}, err
	}
	tools := append([]tool.Tool(nil), snap.tools...)
	if snap.toolAugmenter != nil {
		sessionEffort := strings.TrimSpace(stringMetadata(metadata, "reasoning_effort"))
		if sessionEffort == "" {
			// A ModelProfile always has an explicit effort capability. Provider
			// models without reasoning selectors materialize that capability as
			// the canonical "none" value even though runtime model metadata omits
			// reasoning_effort entirely.
			sessionEffort = "none"
		}
		augmentation, err := snap.toolAugmenter(ctx, ToolAugmentContext{
			SessionRef: intent.SessionRef,
			State:      cloneMap(state),
			Session: controlplacement.SessionContext{
				ProfileID: modelResolution.ProfileID,
				Effort:    sessionEffort,
			},
		})
		if err != nil {
			return agent.AgentSpec{}, err
		}
		tools = append(tools, augmentation.Tools...)
		for key, value := range augmentation.Metadata {
			if strings.TrimSpace(key) == "" {
				continue
			}
			metadata[key] = value
		}
	}
	return agent.AgentSpec{
		Name:     snap.agentName,
		Model:    modelResolution.Model,
		Tools:    tools,
		Metadata: metadata,
	}, nil
}

func resolveMetadataWith(baseMetadata map[string]any, resolved assembly.ResolvedAssembly, intent TurnIntent, state map[string]any, modelResolution ModelResolution) (map[string]any, error) {
	metadata := cloneMap(baseMetadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if err := applyAssemblySelections(metadata, resolved, strings.TrimSpace(intent.ModeName), state); err != nil {
		return nil, err
	}
	if policyProfile := CurrentPolicyProfile(state); policyProfile != "" {
		metadata[policyapi.MetadataPolicyProfile] = policyProfile
		delete(metadata, policyapi.MetadataLegacyPolicyMode)
	} else if profile := metadataPolicyProfile(metadata); profile != "" {
		metadata[policyapi.MetadataPolicyProfile] = profile
		delete(metadata, policyapi.MetadataLegacyPolicyMode)
	} else {
		delete(metadata, policyapi.MetadataLegacyPolicyMode)
	}
	if reasoning := firstNonEmptyString(
		CurrentReasoningEffort(state),
		stringMetadata(metadata, "reasoning_effort"),
		modelResolution.ReasoningEffort,
		modelResolution.DefaultReasoningEffort,
	); reasoning != "" {
		metadata["reasoning_effort"] = reasoning
	}
	if len(metadata) == 0 {
		return map[string]any{}, nil
	}
	return metadata, nil
}

// CurrentModelAlias returns the selected per-session model reference from one
// session state snapshot. The value may be a stable model ID or a legacy alias.
func CurrentModelAlias(state map[string]any) string {
	if state == nil {
		return ""
	}
	value, _ := state[StateCurrentModelAlias].(string)
	return strings.TrimSpace(value)
}

// CurrentReasoningEffort returns the selected per-session reasoning override
// from one session state snapshot.
func CurrentReasoningEffort(state map[string]any) string {
	if state == nil {
		return ""
	}
	value, _ := state[StateCurrentReasoningEffort].(string)
	return strings.TrimSpace(value)
}

// CurrentSessionMode returns the normalized per-session approval routing mode.
func CurrentSessionMode(state map[string]any) string {
	return string(CurrentApprovalMode(state))
}

func CurrentSessionModeOrDefault(state map[string]any, fallback string) string {
	return string(CurrentApprovalModeOrDefault(state, NormalizeApprovalMode(fallback)))
}

func CurrentPolicyProfile(state map[string]any) string {
	if state == nil {
		return ""
	}
	value, _ := state[StateCurrentPolicyProfile].(string)
	return policyapi.NormalizeProfileName(value)
}

func currentApprovalModeOverride(state map[string]any) (ApprovalMode, bool) {
	if state == nil {
		return ApprovalModeAutoReview, false
	}
	if value, _ := state[StateCurrentApprovalMode].(string); strings.TrimSpace(value) != "" {
		return NormalizeApprovalMode(value), true
	}
	return ApprovalModeAutoReview, false
}

func unsupportedLegacyStateKey(state map[string]any) string {
	for _, key := range unsupportedLegacyStateKeys {
		if value, _ := state[key].(string); strings.TrimSpace(value) != "" {
			return key
		}
	}
	return ""
}

func normalizeSessionMode(mode string) string {
	return string(NormalizeApprovalMode(mode))
}

func applyAssemblySelections(metadata map[string]any, resolved assembly.ResolvedAssembly, requestedMode string, state map[string]any) error {
	if len(resolved.Modes) == 0 && len(resolved.Configs) == 0 {
		return nil
	}

	modeID := requestedMode
	if modeID == "" {
		modeID = assembly.CurrentModeID(state)
	}
	if modeID == "" {
		modeID = defaultAssemblyModeID(resolved)
	}
	if modeID != "" {
		mode, ok := assembly.LookupMode(resolved, modeID)
		if !ok {
			return &Error{
				Kind:        KindValidation,
				Code:        CodeModeNotFound,
				UserVisible: true,
				Message:     fmt.Sprintf("gateway: unknown mode %q", modeID),
			}
		}
		applyRuntimeOverrides(metadata, mode.Runtime)
	}

	for _, selection := range assemblyConfigSelections(resolved, state) {
		option, ok := assembly.LookupConfigSelectOption(resolved, selection.ID, selection.Value)
		if !ok {
			continue
		}
		applyRuntimeOverrides(metadata, option.Runtime)
	}
	return nil
}

func dedupeOrderedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

type assemblyConfigSelection struct {
	ID    string
	Value string
}

func assemblyConfigSelections(resolved assembly.ResolvedAssembly, state map[string]any) []assemblyConfigSelection {
	selected := assembly.CurrentConfigValues(state)
	out := make([]assemblyConfigSelection, 0, len(resolved.Configs))
	for _, config := range resolved.Configs {
		configID := strings.TrimSpace(config.ID)
		if configID == "" {
			continue
		}
		value := assemblyConfigValue(config, strings.TrimSpace(selected[configID]))
		if value == "" {
			continue
		}
		out = append(out, assemblyConfigSelection{ID: configID, Value: value})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func assemblyConfigValue(config assembly.ConfigOption, selected string) string {
	if assemblyConfigHasValue(config, selected) {
		return selected
	}
	if def := strings.TrimSpace(config.DefaultValue); assemblyConfigHasValue(config, def) {
		return def
	}
	for _, option := range config.Options {
		if value := strings.TrimSpace(option.Value); value != "" {
			return value
		}
	}
	return ""
}

func assemblyConfigHasValue(config assembly.ConfigOption, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, option := range config.Options {
		if strings.TrimSpace(option.Value) == value {
			return true
		}
	}
	return false
}

func defaultAssemblyModeID(resolved assembly.ResolvedAssembly) string {
	for _, one := range resolved.Modes {
		if strings.EqualFold(strings.TrimSpace(one.ID), "default") {
			return strings.TrimSpace(one.ID)
		}
	}
	for _, one := range resolved.Modes {
		if trimmed := strings.TrimSpace(one.ID); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func applyRuntimeOverrides(metadata map[string]any, overrides assembly.RuntimeOverrides) {
	if metadata == nil {
		return
	}
	if profile := policyapi.NormalizeProfileName(overrides.PolicyMode); profile != "" {
		metadata[policyapi.MetadataPolicyProfile] = profile
		delete(metadata, policyapi.MetadataLegacyPolicyMode)
	}
	if trimmed := strings.TrimSpace(overrides.SystemPrompt); trimmed != "" {
		metadata["system_prompt"] = trimmed
	}
	if trimmed := strings.TrimSpace(overrides.Reasoning.Effort); trimmed != "" {
		metadata["reasoning_effort"] = trimmed
	}
	if overrides.Reasoning.BudgetTokens > 0 {
		metadata["reasoning_budget_tokens"] = overrides.Reasoning.BudgetTokens
	}
	if len(overrides.ExtraReadRoots) > 0 {
		metadata["policy_extra_read_roots"] = mergeStringSliceMetadata(metadata["policy_extra_read_roots"], overrides.ExtraReadRoots)
	}
	if len(overrides.ExtraWriteRoots) > 0 {
		metadata["policy_extra_write_roots"] = mergeStringSliceMetadata(metadata["policy_extra_write_roots"], overrides.ExtraWriteRoots)
	}
}

func mergeStringSliceMetadata(existing any, values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	appendOne := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	switch typed := existing.(type) {
	case []string:
		for _, one := range typed {
			appendOne(one)
		}
	case []any:
		for _, one := range typed {
			text, _ := one.(string)
			appendOne(text)
		}
	}
	for _, one := range values {
		appendOne(one)
	}
	return out
}

func metadataPolicyProfile(metadata map[string]any) string {
	return policyapi.NormalizeProfileName(stringMetadata(metadata, policyapi.MetadataPolicyProfile))
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
