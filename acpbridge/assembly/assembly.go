package assembly

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/acp"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// ProviderConfig configures one set of app-owned ACP providers built from pure
// assembly data. When Sessions is set, current mode/config selections are kept
// in durable session state; otherwise providers fall back to in-memory state.
type ProviderConfig struct {
	Assembly sdkplugin.ResolvedAssembly
	Sessions sdksession.Service
	AppName  string
	UserID   string
}

// ProvidersFromAssembly builds app-owned ACP mode/config providers from one
// pure resolved assembly. When the assembly does not declare a capability, the
// returned provider is nil.
func ProvidersFromAssembly(cfg ProviderConfig) (acp.ModeProvider, acp.ConfigProvider) {
	assembly := sdkplugin.CloneResolvedAssembly(cfg.Assembly)
	var modes acp.ModeProvider
	var configs acp.ConfigProvider
	if len(assembly.Modes) > 0 {
		modes = newModeProvider(assembly.Modes, cfg.Sessions, cfg.AppName, cfg.UserID)
	}
	if len(assembly.Configs) > 0 {
		configs = newConfigProvider(assembly.Configs, cfg.Sessions, cfg.AppName, cfg.UserID)
	}
	return modes, configs
}

// SkillBundles returns normalized pure skill-bundle declarations. Empty roots
// are dropped. Empty namespaces default to the plugin name.
func SkillBundles(assembly sdkplugin.ResolvedAssembly) []sdkplugin.SkillBundle {
	assembly = sdkplugin.CloneResolvedAssembly(assembly)
	if len(assembly.Skills) == 0 {
		return nil
	}
	out := make([]sdkplugin.SkillBundle, 0, len(assembly.Skills))
	for _, one := range assembly.Skills {
		root := strings.TrimSpace(one.Root)
		if root == "" {
			continue
		}
		bundle := sdkplugin.CloneSkillBundle(one)
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
	available []acp.SessionMode
	defaultID string
	sessions  sdksession.Service
	appName   string
	userID    string

	mu      sync.RWMutex
	current map[string]string
}

func newModeProvider(modes []sdkplugin.ModeConfig, sessions sdksession.Service, appName string, userID string) *modeProvider {
	available := make([]acp.SessionMode, 0, len(modes))
	defaultID := ""
	for _, one := range modes {
		id := strings.TrimSpace(one.ID)
		if id == "" {
			continue
		}
		mode := acp.SessionMode{
			ID:          id,
			Name:        strings.TrimSpace(one.Name),
			Description: strings.TrimSpace(one.Description),
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

func (p *modeProvider) SessionModes(ctx context.Context, session sdksession.Session) (*acp.SessionModeState, error) {
	if p == nil || len(p.available) == 0 {
		return &acp.SessionModeState{}, nil
	}
	currentID := p.defaultID
	selected, err := p.currentModeID(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(selected) != "" {
		currentID = selected
	}
	return &acp.SessionModeState{
		AvailableModes: append([]acp.SessionMode(nil), p.available...),
		CurrentModeID:  currentID,
	}, nil
}

func (p *modeProvider) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if p == nil {
		return acp.SetSessionModeResponse{}, acp.ErrCapabilityUnsupported
	}
	sessionID := strings.TrimSpace(req.SessionID)
	modeID := strings.TrimSpace(req.ModeID)
	if sessionID == "" {
		return acp.SetSessionModeResponse{}, fmt.Errorf("acpbridge/assembly: session id is required")
	}
	if modeID == "" {
		return acp.SetSessionModeResponse{}, fmt.Errorf("acpbridge/assembly: mode id is required")
	}
	if !p.hasMode(modeID) {
		return acp.SetSessionModeResponse{}, fmt.Errorf("acpbridge/assembly: mode %q not found", modeID)
	}
	if p.sessions != nil {
		if err := p.sessions.UpdateState(ctx, sessionRef(p.appName, p.userID, sessionID), func(state map[string]any) (map[string]any, error) {
			return sdkplugin.SetCurrentModeID(state, modeID), nil
		}); err != nil {
			return acp.SetSessionModeResponse{}, err
		}
		return acp.SetSessionModeResponse{}, nil
	}
	p.mu.Lock()
	p.current[sessionID] = modeID
	p.mu.Unlock()
	return acp.SetSessionModeResponse{}, nil
}

func (p *modeProvider) hasMode(modeID string) bool {
	for _, one := range p.available {
		if one.ID == modeID {
			return true
		}
	}
	return false
}

func (p *modeProvider) currentModeID(ctx context.Context, ref sdksession.SessionRef) (string, error) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return "", nil
	}
	if p.sessions != nil {
		state, err := p.sessions.SnapshotState(ctx, normalizeSessionRef(ref, p.appName, p.userID))
		if err != nil {
			return "", err
		}
		return sdkplugin.CurrentModeID(state), nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return strings.TrimSpace(p.current[sessionID]), nil
}

type configProvider struct {
	configs  []sdkplugin.ConfigOption
	sessions sdksession.Service
	appName  string
	userID   string

	mu      sync.RWMutex
	current map[string]map[string]string
}

func newConfigProvider(configs []sdkplugin.ConfigOption, sessions sdksession.Service, appName string, userID string) *configProvider {
	out := make([]sdkplugin.ConfigOption, 0, len(configs))
	for _, one := range configs {
		id := strings.TrimSpace(one.ID)
		if id == "" {
			continue
		}
		cfg := sdkplugin.CloneConfigOption(one)
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

func (p *configProvider) SessionConfigOptions(ctx context.Context, session sdksession.Session) ([]acp.SessionConfigOption, error) {
	if p == nil || len(p.configs) == 0 {
		return nil, nil
	}
	selected, err := p.currentValues(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	return p.renderOptions(selected), nil
}

func (p *configProvider) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.ErrCapabilityUnsupported
	}
	sessionID := strings.TrimSpace(req.SessionID)
	configID := strings.TrimSpace(req.ConfigID)
	if sessionID == "" {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpbridge/assembly: session id is required")
	}
	if configID == "" {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpbridge/assembly: config id is required")
	}
	value, ok := req.Value.(string)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpbridge/assembly: config value for %q must be a string", configID)
	}
	cfg, ok := p.lookup(configID)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpbridge/assembly: config %q not found", configID)
	}
	value = strings.TrimSpace(value)
	if !hasConfigValue(cfg, value) {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpbridge/assembly: invalid value %q for config %q", value, configID)
	}
	if p.sessions != nil {
		ref := sessionRef(p.appName, p.userID, sessionID)
		if err := p.sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
			return sdkplugin.SetCurrentConfigValue(state, configID, value), nil
		}); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		selected, err := p.currentValues(ctx, ref)
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		return acp.SetSessionConfigOptionResponse{
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
	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: p.renderOptions(selected),
	}, nil
}

func (p *configProvider) lookup(configID string) (sdkplugin.ConfigOption, bool) {
	for _, one := range p.configs {
		if one.ID == configID {
			return sdkplugin.CloneConfigOption(one), true
		}
	}
	return sdkplugin.ConfigOption{}, false
}

func (p *configProvider) renderOptions(selected map[string]string) []acp.SessionConfigOption {
	out := make([]acp.SessionConfigOption, 0, len(p.configs))
	for _, one := range p.configs {
		value := strings.TrimSpace(selected[one.ID])
		if value == "" || !hasConfigValue(one, value) {
			value = one.DefaultValue
		}
		options := make([]acp.SessionConfigSelectOption, 0, len(one.Options))
		for _, option := range one.Options {
			options = append(options, acp.SessionConfigSelectOption{
				Value:       option.Value,
				Name:        option.Name,
				Description: option.Description,
			})
		}
		out = append(out, acp.SessionConfigOption{
			Type:         "select",
			ID:           one.ID,
			Name:         one.Name,
			Description:  one.Description,
			Category:     one.Category,
			CurrentValue: value,
			Options:      options,
		})
	}
	return out
}

func (p *configProvider) currentValues(ctx context.Context, ref sdksession.SessionRef) (map[string]string, error) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return map[string]string{}, nil
	}
	if p.sessions != nil {
		state, err := p.sessions.SnapshotState(ctx, normalizeSessionRef(ref, p.appName, p.userID))
		if err != nil {
			return nil, err
		}
		return sdkplugin.CurrentConfigValues(state), nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return mapsCloneStringMap(p.current[sessionID]), nil
}

func hasConfigValue(config sdkplugin.ConfigOption, value string) bool {
	return slices.ContainsFunc(config.Options, func(option sdkplugin.ConfigSelectOption) bool {
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

func normalizeSessionRef(ref sdksession.SessionRef, appName string, userID string) sdksession.SessionRef {
	ref = sdksession.NormalizeSessionRef(ref)
	if ref.AppName == "" {
		ref.AppName = strings.TrimSpace(appName)
	}
	if ref.UserID == "" {
		ref.UserID = strings.TrimSpace(userID)
	}
	return ref
}

func sessionRef(appName string, userID string, sessionID string) sdksession.SessionRef {
	return normalizeSessionRef(sdksession.SessionRef{
		AppName:   strings.TrimSpace(appName),
		UserID:    strings.TrimSpace(userID),
		SessionID: strings.TrimSpace(sessionID),
	}, appName, userID)
}
