package gatewayapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/agentprofiles"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type AgentProfileService struct {
	stack *Stack
}

type AgentProfileLoadWarning struct {
	Path    string
	Message string
}

type AgentProfileStatus struct {
	Profiles []agentprofile.Snapshot
	Warnings []AgentProfileLoadWarning
}

type AgentProfileBindingConfig struct {
	ProfileID       string
	Target          agentprofile.BindingTargetKind
	Model           string
	ACPAgent        string
	ACPModel        string
	ReasoningEffort string
}

const guardianProfileID = "guardian"

func (s *Stack) AgentProfiles() AgentProfileService {
	return AgentProfileService{stack: s}
}

func (s *Stack) newModelApprovalReviewer() gateway.ApprovalReviewer {
	return guardianBindingApprovalReviewer{
		base:    newModelApprovalReviewer(s.Sessions),
		resolve: s.resolveGuardianReviewModel,
	}
}

func (s AgentProfileService) Status(ctx context.Context) (AgentProfileStatus, error) {
	if s.stack == nil {
		return AgentProfileStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return AgentProfileStatus{}, err
		}
	}
	return s.stack.agentProfileStatus(ctx)
}

func (s AgentProfileService) Bind(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatus, error) {
	if s.stack == nil || s.stack.store == nil {
		return AgentProfileStatus{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AgentProfileStatus{}, err
	}
	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("bind subagent profile"); err != nil {
		return AgentProfileStatus{}, err
	}
	profiles, _, err := s.stack.loadAgentProfiles()
	if err != nil {
		return AgentProfileStatus{}, err
	}
	profileID := normalizeAgentProfileID(cfg.ProfileID)
	if _, ok := profiles[profileID]; !ok && profileID != guardianProfileID {
		return AgentProfileStatus{}, fmt.Errorf("gatewayapp: unknown subagent profile %q", strings.TrimSpace(cfg.ProfileID))
	}
	binding := agentprofile.NormalizeBinding(agentprofile.Binding{
		ProfileID:       profileID,
		Target:          cfg.Target,
		Model:           cfg.Model,
		ACPAgent:        cfg.ACPAgent,
		ACPModel:        cfg.ACPModel,
		ReasoningEffort: cfg.ReasoningEffort,
		Enabled:         boolPtr(true),
	})
	if err := s.stack.validateAgentProfileBinding(binding); err != nil {
		return AgentProfileStatus{}, err
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return AgentProfileStatus{}, err
	}
	now := time.Now()
	next, err := agentprofile.UpsertBinding(doc.AgentBindings, binding, now)
	if err != nil {
		return AgentProfileStatus{}, err
	}
	doc.AgentBindings = next
	if err := s.stack.store.Save(doc); err != nil {
		return AgentProfileStatus{}, err
	}
	if err := s.stack.setConfiguredAgents(doc.Agents); err != nil {
		return AgentProfileStatus{}, err
	}
	return s.stack.agentProfileStatus(ctx)
}

func ensureBuiltInAgentProfiles(ctx context.Context, storeDir string, store *appConfigStore) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if store == nil {
		return nil
	}
	agentsDir := filepath.Join(strings.TrimSpace(storeDir), agentprofiles.DefaultAgentsDirName)
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		return err
	}
	status, err := agentprofiles.LoadDirStatus(agentsDir)
	if err != nil {
		return err
	}
	loaded := map[string]struct{}{}
	for _, profile := range status.Profiles {
		if id := strings.TrimSpace(profile.ID); id != "" {
			loaded[id] = struct{}{}
		}
	}
	for _, profile := range builtInAgentProfiles() {
		if _, ok := loaded[profile.ID]; ok {
			continue
		}
		path := filepath.Join(agentsDir, profile.ID+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		profile.Path = path
		if err := atomicWriteFile(path, []byte(agentprofile.FormatMarkdown(profile)), 0o600, atomicWriteOps{}); err != nil {
			return err
		}
	}
	doc, err := store.Load()
	if err != nil {
		return err
	}
	changed := false
	now := time.Now()
	for _, profile := range builtInAgentProfiles() {
		if _, ok := agentprofile.LookupBinding(doc.AgentBindings, profile.ID); ok {
			continue
		}
		next, err := agentprofile.UpsertBinding(doc.AgentBindings, agentprofile.Binding{
			ProfileID: profile.ID,
			Target:    agentprofile.BindingTargetBuiltIn,
			Enabled:   boolPtr(true),
		}, now)
		if err != nil {
			return err
		}
		doc.AgentBindings = next
		changed = true
	}
	if changed {
		return store.Save(doc)
	}
	return nil
}

func (s *Stack) agentProfileStatus(ctx context.Context) (AgentProfileStatus, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return AgentProfileStatus{}, err
		}
	}
	profiles, warnings, err := s.loadAgentProfiles()
	if err != nil {
		return AgentProfileStatus{}, err
	}
	doc, err := s.store.Load()
	if err != nil {
		return AgentProfileStatus{}, err
	}
	out := AgentProfileStatus{Warnings: warnings}
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		profile := profiles[id]
		binding, ok := agentprofile.LookupBinding(doc.AgentBindings, id)
		if !ok {
			binding = defaultAgentProfileBinding(id)
		}
		binding = s.annotateAgentProfileBinding(binding)
		out.Profiles = append(out.Profiles, agentprofile.Snapshot{
			Profile: profile,
			Binding: binding,
		})
	}
	guardianBinding, ok := agentprofile.LookupBinding(doc.AgentBindings, guardianProfileID)
	if !ok {
		guardianBinding = defaultAgentProfileBinding(guardianProfileID)
	}
	guardianBinding = normalizeGuardianProfileBinding(guardianBinding)
	out.Profiles = append(out.Profiles, agentprofile.Snapshot{
		Profile: guardianVirtualProfile(),
		Binding: s.annotateAgentProfileBinding(guardianBinding),
	})
	sort.SliceStable(out.Profiles, func(i, j int) bool {
		return strings.ToLower(out.Profiles[i].Profile.ID) < strings.ToLower(out.Profiles[j].Profile.ID)
	})
	return out, nil
}

func (s *Stack) loadAgentProfiles() (map[string]agentprofile.Profile, []AgentProfileLoadWarning, error) {
	status, err := agentprofiles.LoadDirStatus(filepath.Join(s.storeDir, agentprofiles.DefaultAgentsDirName))
	if err != nil {
		return nil, nil, err
	}
	profiles := make(map[string]agentprofile.Profile, len(status.Profiles))
	for _, profile := range status.Profiles {
		profile = agentprofile.NormalizeProfile(profile)
		if profile.ID == guardianProfileID {
			continue
		}
		if profile.ID != "" {
			profiles[profile.ID] = profile
		}
	}
	warnings := make([]AgentProfileLoadWarning, 0, len(status.Warnings))
	for _, warning := range status.Warnings {
		warnings = append(warnings, AgentProfileLoadWarning{
			Path:    warning.Path,
			Message: warning.Message,
		})
	}
	return profiles, warnings, nil
}

func (s *Stack) validateAgentProfileBinding(binding agentprofile.Binding) error {
	binding = agentprofile.NormalizeBinding(binding)
	if err := agentprofile.ValidateBinding(binding); err != nil {
		return err
	}
	switch binding.Target {
	case agentprofile.BindingTargetSelf, agentprofile.BindingTargetBuiltIn:
		if binding.Model != "" {
			cfg, err := s.lookup.ResolveConfig(binding.Model)
			if err != nil {
				return err
			}
			if binding.ReasoningEffort != "" && !modelConfigSupportsReasoningEffort(cfg, binding.ReasoningEffort) {
				return fmt.Errorf("gatewayapp: model %q does not support reasoning level %q", binding.Model, binding.ReasoningEffort)
			}
		}
	case agentprofile.BindingTargetACP:
		if binding.ProfileID == guardianProfileID {
			return fmt.Errorf("gatewayapp: guardian cannot bind to an external ACP agent")
		}
		if !s.acpAgentExists(binding.ACPAgent) {
			return fmt.Errorf("gatewayapp: unknown ACP agent %q", binding.ACPAgent)
		}
	}
	return nil
}

func (s *Stack) annotateAgentProfileBinding(binding agentprofile.Binding) agentprofile.Binding {
	binding = agentprofile.NormalizeBinding(binding)
	binding.Status = agentprofile.BindingStatusOK
	binding.Warning = ""
	switch binding.Target {
	case agentprofile.BindingTargetSelf, agentprofile.BindingTargetBuiltIn:
		if binding.Model != "" {
			cfg, err := s.lookup.ResolveConfig(binding.Model)
			if err != nil {
				binding.Status = agentprofile.BindingStatusStale
				binding.Warning = err.Error()
				return binding
			}
			if binding.ReasoningEffort != "" && !modelConfigSupportsReasoningEffort(cfg, binding.ReasoningEffort) {
				binding.Status = agentprofile.BindingStatusWarning
				binding.Warning = fmt.Sprintf("model %q does not support reasoning level %q", binding.Model, binding.ReasoningEffort)
			}
		}
	case agentprofile.BindingTargetACP:
		if !s.acpAgentExists(binding.ACPAgent) {
			binding.Status = agentprofile.BindingStatusStale
			binding.Warning = fmt.Sprintf("ACP agent %q is not registered", binding.ACPAgent)
		}
	}
	if s.agentProfileNameConflictsWithACPAgent(binding.ProfileID) {
		binding.Status = agentprofile.BindingStatusStale
		binding.Warning = fmt.Sprintf("profile name %q conflicts with a registered ACP agent", binding.ProfileID)
	}
	return binding
}

func (s *Stack) agentProfileNameConflictsWithACPAgent(profileID string) bool {
	profileID = normalizeAgentProfileID(profileID)
	if profileID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, agent := range s.runtime.Assembly.Agents {
		if !strings.EqualFold(strings.TrimSpace(agent.Name), profileID) {
			continue
		}
		if isSubagentProfileAgent(agent) {
			continue
		}
		return true
	}
	return false
}

func (s *Stack) acpAgentExists(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "self") {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, agent := range s.runtime.Assembly.Agents {
		if isSubagentProfileAgent(agent) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return true
		}
	}
	return false
}

func (s *Stack) resolveGuardianReviewModel(ctx context.Context, _ session.SessionRef) (model.LLM, error) {
	if s == nil || s.store == nil || s.lookup == nil {
		return nil, nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	binding, ok := agentprofile.LookupBinding(doc.AgentBindings, guardianProfileID)
	if !ok {
		return nil, nil
	}
	binding = normalizeGuardianProfileBinding(binding)
	if strings.TrimSpace(binding.Model) == "" {
		return nil, nil
	}
	cfg, err := s.lookup.ResolveConfig(binding.Model)
	if err != nil {
		return nil, err
	}
	if reasoning := strings.TrimSpace(binding.ReasoningEffort); reasoning != "" {
		cfg.ReasoningEffort = reasoning
		cfg.DefaultReasoningEffort = reasoning
	}
	s.mu.RLock()
	contextWindow := s.runtime.ContextWindow
	s.mu.RUnlock()
	resolved, err := s.lookup.ResolveModelConfig(ctx, cfg, contextWindow)
	if err != nil {
		return nil, err
	}
	return resolved.Model, nil
}

func defaultAgentProfileBinding(profileID string) agentprofile.Binding {
	return agentprofile.NormalizeBinding(agentprofile.Binding{
		ProfileID: normalizeAgentProfileID(profileID),
		Target:    agentprofile.BindingTargetBuiltIn,
		Enabled:   boolPtr(true),
	})
}

func normalizeGuardianProfileBinding(binding agentprofile.Binding) agentprofile.Binding {
	binding.ProfileID = guardianProfileID
	binding = agentprofile.NormalizeBinding(binding)
	binding.Enabled = boolPtr(true)
	binding.Status = agentprofile.BindingStatusOK
	binding.Warning = ""
	if binding.Target == agentprofile.BindingTargetACP {
		binding.Target = agentprofile.BindingTargetSelf
		binding.Model = ""
		binding.ACPAgent = ""
		binding.ACPModel = ""
		binding.ReasoningEffort = ""
	}
	return binding
}

func normalizeAgentProfileID(value string) string {
	return agentprofile.NormalizeProfile(agentprofile.Profile{ID: value, Description: "x"}).ID
}

func builtInAgentProfiles() []agentprofile.Profile {
	return []agentprofile.Profile{
		{
			ID:           "explorer",
			Name:         "Explorer",
			Description:  "Explore code and gather evidence before implementation.",
			Capabilities: []string{"search", "analysis"},
			Instructions: strings.TrimSpace(`
You are an exploration subagent. Inspect the requested code or runtime path, gather concrete evidence, and report concise findings with file references. Do not make code changes unless explicitly requested by the parent agent.
`),
			Metadata: map[string]any{"source": "caelis", "built_in": true},
		},
		{
			ID:           "reviewer",
			Name:         "Reviewer",
			Description:  "Review a change for bugs, regressions, and missing validation.",
			Capabilities: []string{"review", "testing"},
			Instructions: strings.TrimSpace(`
You are a code review subagent. Prioritize correctness bugs, regression risks, boundary violations, and missing tests. Return findings first, ordered by severity, with concrete file and line references where possible.
`),
			Metadata: map[string]any{"source": "caelis", "built_in": true},
		},
	}
}

func guardianVirtualProfile() agentprofile.Profile {
	return agentprofile.NormalizeProfile(agentprofile.Profile{
		ID:          guardianProfileID,
		Name:        "Guardian",
		Description: "Reviews approval requests for auto-review mode.",
		Metadata: map[string]any{
			"source":         "caelis",
			"built_in":       true,
			"system_managed": true,
		},
	})
}
