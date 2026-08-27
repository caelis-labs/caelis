package assembly

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ProviderConfig configures one set of app-owned ACP providers built from pure
// assembly data. When Sessions is set, current mode/config selections are kept
// in durable session state; otherwise providers fall back to in-memory state.
type ProviderConfig struct {
	Assembly assembly.ResolvedAssembly
	Sessions session.Service
	AppName  string
	UserID   string
}

// ModeReader exposes only the assembly-backed ACP mode projection.
type ModeReader interface {
	SessionModes(context.Context, session.Session) (*acpsdk.SessionModeState, error)
}

// ModeWriter exposes only the assembly-backed ACP mode mutation.
type ModeWriter interface {
	SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error)
}

// ConfigReader exposes only the assembly-backed ACP config projection.
type ConfigReader interface {
	SessionConfigOptions(context.Context, session.Session) ([]acpsdk.SessionConfigOption, error)
}

// ConfigWriter exposes only the assembly-backed ACP config mutation.
type ConfigWriter interface {
	SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error)
}

// Providers is a capability set, not an aggregate provider. Each absent
// capability remains a nil interface when the resolved assembly omits it.
type Providers struct {
	Modes        ModeReader
	ModeWriter   ModeWriter
	Config       ConfigReader
	ConfigWriter ConfigWriter
}

// ProvidersFromAssembly builds app-owned ACP mode/config providers from one
// pure resolved assembly. When the assembly does not declare a capability, the
// returned provider is nil.
func ProvidersFromAssembly(cfg ProviderConfig) Providers {
	resolved := assembly.CloneResolvedAssembly(cfg.Assembly)
	providers := Providers{}
	if len(resolved.Modes) > 0 {
		modes := newModeProvider(resolved.Modes, cfg.Sessions, cfg.AppName, cfg.UserID)
		providers.Modes = modes
		providers.ModeWriter = modes
	}
	if len(resolved.Configs) > 0 {
		configs := newConfigProvider(resolved.Configs, cfg.Sessions, cfg.AppName, cfg.UserID)
		providers.Config = configs
		providers.ConfigWriter = configs
	}
	return providers
}

// SkillBundles returns normalized pure skill-bundle declarations. Empty roots
// are dropped. Empty namespaces default to the plugin name.
func SkillBundles(resolved assembly.ResolvedAssembly) []assembly.SkillBundle {
	resolved = assembly.CloneResolvedAssembly(resolved)
	if len(resolved.Skills) == 0 {
		return nil
	}
	out := make([]assembly.SkillBundle, 0, len(resolved.Skills))
	for _, one := range resolved.Skills {
		root := strings.TrimSpace(one.Root)
		if root == "" {
			continue
		}
		bundle := assembly.CloneSkillBundle(one)
		bundle.Plugin = strings.TrimSpace(bundle.Plugin)
		bundle.Root = root
		bundle.Namespace = strings.TrimSpace(bundle.Namespace)
		if bundle.Namespace == "" {
			bundle.Namespace = bundle.Plugin
		}
		for i, disabled := range bundle.Disabled {
			bundle.Disabled[i] = strings.TrimSpace(disabled)
		}
		out = append(out, bundle)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type modeProvider struct {
	available []acpsdk.SessionMode
	defaultID string
	sessions  session.Service
	appName   string
	userID    string

	mu      sync.RWMutex
	current map[string]string
}

func newModeProvider(modes []assembly.ModeConfig, sessions session.Service, appName string, userID string) *modeProvider {
	available := make([]acpsdk.SessionMode, 0, len(modes))
	defaultID := ""
	for _, one := range modes {
		id := strings.TrimSpace(one.ID)
		if id == "" {
			continue
		}
		mode := acpsdk.SessionMode{
			Id:   acpsdk.SessionModeId(id),
			Name: strings.TrimSpace(one.Name),
		}
		if description := strings.TrimSpace(one.Description); description != "" {
			mode.Description = &description
		}
		if mode.Name == "" {
			mode.Name = id
		}
		if defaultID == "" || strings.EqualFold(id, "default") {
			defaultID = id
		}
		available = append(available, mode)
	}
	if len(available) == 0 {
		return nil
	}
	return &modeProvider{
		available: available,
		defaultID: defaultID,
		sessions:  sessions,
		appName:   strings.TrimSpace(appName),
		userID:    strings.TrimSpace(userID),
		current:   map[string]string{},
	}
}

func (p *modeProvider) SessionModes(ctx context.Context, session session.Session) (*acpsdk.SessionModeState, error) {
	if p == nil || len(p.available) == 0 {
		return &acpsdk.SessionModeState{}, nil
	}
	currentID := p.defaultID
	selected, err := p.currentModeID(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(selected) != "" {
		currentID = selected
	}
	return &acpsdk.SessionModeState{
		AvailableModes: append([]acpsdk.SessionMode(nil), p.available...),
		CurrentModeId:  acpsdk.SessionModeId(currentID),
	}, nil
}

func (p *modeProvider) SetSessionMode(ctx context.Context, req acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	if p == nil {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: mode provider is unavailable")
	}
	sessionID := strings.TrimSpace(string(req.SessionId))
	modeID := strings.TrimSpace(string(req.ModeId))
	if sessionID == "" {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: session id is required")
	}
	if modeID == "" {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: mode id is required")
	}
	if !p.hasMode(modeID) {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: mode %q not found", modeID)
	}
	if p.sessions != nil {
		ref, err := resolveProviderSessionRef(ctx, p.sessions, p.appName, p.userID, sessionID)
		if err != nil {
			return acpsdk.SetSessionModeResponse{}, err
		}
		if err := updateProviderSessionState(ctx, p.sessions, ref, func(state map[string]any) (map[string]any, error) {
			return assembly.SetCurrentModeID(state, modeID), nil
		}); err != nil {
			return acpsdk.SetSessionModeResponse{}, err
		}
		return acpsdk.SetSessionModeResponse{}, nil
	}
	p.mu.Lock()
	p.current[sessionID] = modeID
	p.mu.Unlock()
	return acpsdk.SetSessionModeResponse{}, nil
}

func (p *modeProvider) hasMode(modeID string) bool {
	for _, one := range p.available {
		if string(one.Id) == modeID {
			return true
		}
	}
	return false
}

func (p *modeProvider) currentModeID(ctx context.Context, ref session.SessionRef) (string, error) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return "", nil
	}
	if p.sessions != nil {
		state, err := p.sessions.SnapshotState(ctx, normalizeSessionRef(ref, p.appName, p.userID))
		if err != nil {
			return "", err
		}
		return assembly.CurrentModeID(state), nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return strings.TrimSpace(p.current[sessionID]), nil
}

type configProvider struct {
	configs  []assembly.ConfigOption
	sessions session.Service
	appName  string
	userID   string

	mu      sync.RWMutex
	current map[string]map[string]string
}

func newConfigProvider(configs []assembly.ConfigOption, sessions session.Service, appName string, userID string) *configProvider {
	out := make([]assembly.ConfigOption, 0, len(configs))
	for _, one := range configs {
		id := strings.TrimSpace(one.ID)
		if id == "" {
			continue
		}
		cfg := assembly.CloneConfigOption(one)
		cfg.ID = id
		cfg.Name = strings.TrimSpace(cfg.Name)
		cfg.Description = strings.TrimSpace(cfg.Description)
		cfg.Category = strings.TrimSpace(cfg.Category)
		cfg.DefaultValue = strings.TrimSpace(cfg.DefaultValue)
		for i, option := range cfg.Options {
			cfg.Options[i].Value = strings.TrimSpace(option.Value)
			cfg.Options[i].Name = strings.TrimSpace(option.Name)
			cfg.Options[i].Description = strings.TrimSpace(option.Description)
			if cfg.Options[i].Name == "" {
				cfg.Options[i].Name = cfg.Options[i].Value
			}
		}
		if len(cfg.Options) == 0 {
			continue
		}
		if cfg.Name == "" {
			cfg.Name = cfg.ID
		}
		if cfg.DefaultValue == "" {
			cfg.DefaultValue = cfg.Options[0].Value
		}
		out = append(out, cfg)
	}
	if len(out) == 0 {
		return nil
	}
	return &configProvider{
		configs:  out,
		sessions: sessions,
		appName:  strings.TrimSpace(appName),
		userID:   strings.TrimSpace(userID),
		current:  map[string]map[string]string{},
	}
}

func (p *configProvider) SessionConfigOptions(ctx context.Context, session session.Session) ([]acpsdk.SessionConfigOption, error) {
	if p == nil || len(p.configs) == 0 {
		return nil, nil
	}
	selected, err := p.currentValues(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	return p.renderOptions(selected), nil
}

func (p *configProvider) SetSessionConfigOption(ctx context.Context, req acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	if p == nil {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: config provider is unavailable")
	}
	if req.ValueId == nil {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: config value must be a string selection")
	}
	sessionID := strings.TrimSpace(string(req.ValueId.SessionId))
	configID := strings.TrimSpace(string(req.ValueId.ConfigId))
	if sessionID == "" {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: session id is required")
	}
	if configID == "" {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: config id is required")
	}
	value := string(req.ValueId.Value)
	cfg, ok := p.lookup(configID)
	if !ok {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: config %q not found", configID)
	}
	value = strings.TrimSpace(value)
	if !hasConfigValue(cfg, value) {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge/assembly: invalid value %q for config %q", value, configID)
	}
	if p.sessions != nil {
		ref, err := resolveProviderSessionRef(ctx, p.sessions, p.appName, p.userID, sessionID)
		if err != nil {
			return acpsdk.SetSessionConfigOptionResponse{}, err
		}
		if err := updateProviderSessionState(ctx, p.sessions, ref, func(state map[string]any) (map[string]any, error) {
			return assembly.SetCurrentConfigValue(state, configID, value), nil
		}); err != nil {
			return acpsdk.SetSessionConfigOptionResponse{}, err
		}
		selected, err := p.currentValues(ctx, ref)
		if err != nil {
			return acpsdk.SetSessionConfigOptionResponse{}, err
		}
		return acpsdk.SetSessionConfigOptionResponse{
			ConfigOptions: p.renderOptions(selected),
		}, nil
	}
	p.mu.Lock()
	if p.current[sessionID] == nil {
		p.current[sessionID] = map[string]string{}
	}
	p.current[sessionID][configID] = value
	selected := mapsCloneStringMap(p.current[sessionID])
	p.mu.Unlock()
	return acpsdk.SetSessionConfigOptionResponse{
		ConfigOptions: p.renderOptions(selected),
	}, nil
}

func updateProviderSessionState(
	ctx context.Context,
	sessions session.Service,
	ref session.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	current, err := sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	_, err = sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:       ref,
		ExpectedRevision: &current.Revision,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Update:           update,
	})
	return err
}

func (p *configProvider) lookup(configID string) (assembly.ConfigOption, bool) {
	for _, one := range p.configs {
		if one.ID == configID {
			return assembly.CloneConfigOption(one), true
		}
	}
	return assembly.ConfigOption{}, false
}

func (p *configProvider) renderOptions(selected map[string]string) []acpsdk.SessionConfigOption {
	out := make([]acpsdk.SessionConfigOption, 0, len(p.configs))
	for _, one := range p.configs {
		value := strings.TrimSpace(selected[one.ID])
		if value == "" || !hasConfigValue(one, value) {
			value = one.DefaultValue
		}
		options := make(acpsdk.SessionConfigSelectOptionsUngrouped, 0, len(one.Options))
		for _, option := range one.Options {
			mapped := acpsdk.SessionConfigSelectOption{Value: acpsdk.SessionConfigValueId(option.Value), Name: option.Name}
			if description := strings.TrimSpace(option.Description); description != "" {
				mapped.Description = &description
			}
			options = append(options, mapped)
		}
		selectOption := acpsdk.SessionConfigOptionSelect{
			Type: "select", Id: acpsdk.SessionConfigId(one.ID), Name: one.Name,
			CurrentValue: acpsdk.SessionConfigValueId(value),
			Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &options},
		}
		if description := strings.TrimSpace(one.Description); description != "" {
			selectOption.Description = &description
		}
		if category := strings.TrimSpace(one.Category); category != "" {
			typed := acpsdk.SessionConfigOptionCategory(category)
			selectOption.Category = &typed
		}
		out = append(out, acpsdk.SessionConfigOption{Select: &selectOption})
	}
	return out
}

func (p *configProvider) currentValues(ctx context.Context, ref session.SessionRef) (map[string]string, error) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return map[string]string{}, nil
	}
	if p.sessions != nil {
		state, err := p.sessions.SnapshotState(ctx, normalizeSessionRef(ref, p.appName, p.userID))
		if err != nil {
			return nil, err
		}
		return assembly.CurrentConfigValues(state), nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return mapsCloneStringMap(p.current[sessionID]), nil
}

func hasConfigValue(config assembly.ConfigOption, value string) bool {
	return slices.ContainsFunc(config.Options, func(option assembly.ConfigSelectOption) bool {
		return option.Value == value
	})
}

func mapsCloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizeSessionRef(ref session.SessionRef, appName string, userID string) session.SessionRef {
	ref = session.NormalizeSessionRef(ref)
	if ref.AppName == "" {
		ref.AppName = strings.TrimSpace(appName)
	}
	if ref.UserID == "" {
		ref.UserID = strings.TrimSpace(userID)
	}
	return ref
}

func sessionRef(appName string, userID string, sessionID string) session.SessionRef {
	return normalizeSessionRef(session.SessionRef{
		AppName:   strings.TrimSpace(appName),
		UserID:    strings.TrimSpace(userID),
		SessionID: strings.TrimSpace(sessionID),
	}, appName, userID)
}

func resolveProviderSessionRef(
	ctx context.Context,
	sessions session.Service,
	appName string,
	userID string,
	sessionID string,
) (session.SessionRef, error) {
	if sessions == nil {
		return sessionRef(appName, userID, sessionID), nil
	}
	ref := sessionRef(appName, userID, sessionID)
	activeSession, err := sessions.Session(ctx, ref)
	if err != nil {
		return session.SessionRef{}, err
	}
	return activeSession.SessionRef, nil
}
