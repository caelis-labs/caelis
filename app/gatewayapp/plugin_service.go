package gatewayapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/pluginregistry"
	"github.com/OnslaughtSnail/caelis/impl/tool/mcp"
)

type PluginService struct {
	stack *Stack
}

func (s *Stack) Plugins() PluginService {
	return PluginService{stack: s}
}

type PluginInfo struct {
	ID          string
	Name        string
	Version     string
	Description string
	Root        string
	Enabled     bool
	Skills      []string
	Hooks       []string
	Agents      []string
	MCPServers  []mcp.MCPServerInfo
	Status      string
	Warning     string
}

func (s PluginService) List(ctx context.Context) ([]PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return nil, fmt.Errorf("plugin service: stack store is unavailable")
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return nil, err
	}

	var list []PluginInfo
	for _, pCfg := range doc.Plugins {
		info := s.pluginInfoFromConfig(pCfg)

		// For disabled plugins skip deep parse – show stored metadata only.
		if !pCfg.Enabled {
			list = append(list, info)
			continue
		}

		s.enrichPluginInfoFromManifest(&info, pCfg)
		list = append(list, info)
	}

	return list, nil
}

// AddPath registers or updates a local plugin directory, enables it, and
// rebuilds the gateway. If the gateway rebuild fails the config is rolled back.
func (s PluginService) AddPath(ctx context.Context, path string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	absPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: failed to resolve absolute path: %w", err)
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return PluginInfo{}, fmt.Errorf("plugin service: path is not a directory: %s", absPath)
	}

	p, err := pluginregistry.ParsePlugin(absPath)
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: parse plugin failed: %w", err)
	}

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("add plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	id := strings.ToLower(filepath.Base(absPath))
	var found bool
	for i, pCfg := range doc.Plugins {
		if strings.ToLower(pCfg.ID) == id {
			doc.Plugins[i].Root = absPath
			doc.Plugins[i].Name = p.Name
			doc.Plugins[i].Version = p.Version
			doc.Plugins[i].Description = p.Description
			doc.Plugins[i].Manifest = p.Manifest
			doc.Plugins[i].Kind = string(p.Kind)
			doc.Plugins[i].Enabled = true
			found = true
			break
		}
	}
	if !found {
		doc.Plugins = append(doc.Plugins, PluginConfig{
			ID:          id,
			Name:        p.Name,
			Root:        absPath,
			Manifest:    p.Manifest,
			Kind:        string(p.Kind),
			Enabled:     true,
			Version:     p.Version,
			Description: p.Description,
		})
	}

	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("rebuild gateway after adding plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Enable marks a registered plugin as enabled and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Enable(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("enable plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	doc.Plugins[foundIdx].Enabled = true
	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("enable plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Disable marks a registered plugin as disabled and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Disable(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("disable plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	doc.Plugins[foundIdx].Enabled = false
	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("disable plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Remove removes a plugin from the registry and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Remove(ctx context.Context, id string) error {
	if s.stack == nil || s.stack.store == nil {
		return fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("remove plugin"); err != nil {
		return err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return err
	}

	doc.Plugins = append(doc.Plugins[:foundIdx], doc.Plugins[foundIdx+1:]...)
	if err := s.stack.store.Save(doc); err != nil {
		return err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return s.handleRebuildError("rebuild gateway after removing plugin", err, oldDoc)
	}

	return nil
}

func (s PluginService) Inspect(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	doc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	pCfg := doc.Plugins[foundIdx]
	info := s.pluginInfoFromConfig(pCfg)
	s.enrichPluginInfoFromManifest(&info, pCfg)

	return info, nil
}

func findPluginConfigIndex(doc AppConfig, id string) (int, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for i, pCfg := range doc.Plugins {
		if strings.ToLower(strings.TrimSpace(pCfg.ID)) == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("plugin service: plugin not found: %s", id)
}

func (s PluginService) pluginInfoFromConfig(pCfg PluginConfig) PluginInfo {
	info := PluginInfo{
		ID:          pCfg.ID,
		Name:        pCfg.Name,
		Version:     pCfg.Version,
		Description: pCfg.Description,
		Root:        pCfg.Root,
		Enabled:     pCfg.Enabled,
		Status:      "inactive",
	}
	if pCfg.Enabled {
		info.Status = "active"
		info.MCPServers = s.stack.MCPServersStatus(pCfg.ID)
	}
	return info
}

func (s PluginService) enrichPluginInfoFromManifest(info *PluginInfo, pCfg PluginConfig) {
	if info == nil {
		return
	}
	p, err := pluginregistry.ParsePlugin(pCfg.Root)
	if err != nil {
		info.Status = "error"
		info.Warning = err.Error()
		return
	}
	info.Name = firstNonEmpty(info.Name, p.Name)
	info.Version = firstNonEmpty(info.Version, p.Version)
	info.Description = firstNonEmpty(info.Description, p.Description)
	for _, sc := range p.Skills {
		info.Skills = append(info.Skills, filepath.Base(sc.Root))
	}
	for _, hook := range p.Hooks {
		info.Hooks = append(info.Hooks, string(hook.Event))
	}
	for _, agent := range p.Agents {
		if name := strings.TrimSpace(agent.Name); name != "" {
			info.Agents = append(info.Agents, name)
		}
	}
	for _, mcpSpec := range p.MCPServers {
		if pluginInfoHasMCPServer(*info, mcpSpec.Name) {
			continue
		}
		status := "stopped"
		if !pCfg.Enabled {
			status = "disabled"
		}
		info.MCPServers = append(info.MCPServers, mcp.MCPServerInfo{
			Name:   mcpSpec.Name,
			Status: status,
		})
	}
}

func pluginInfoHasMCPServer(info PluginInfo, name string) bool {
	for _, live := range info.MCPServers {
		if live.Name == name {
			return true
		}
	}
	return false
}

// cloneAppConfig returns a shallow clone of AppConfig with a fresh Plugins
// slice so mutations do not affect the original snapshot.
func cloneAppConfig(doc AppConfig) AppConfig {
	out := doc
	out.Plugins = clonePluginConfigs(doc.Plugins)
	return out
}

func (s PluginService) handleRebuildError(action string, err error, oldDoc AppConfig) error {
	rollbackErr := s.stack.store.Save(oldDoc)
	if rollbackErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback config save failed): %w, rollback error: %w", action, err, rollbackErr)
	}
	if rbRebuildErr := s.stack.rebuildGateway(); rbRebuildErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback rebuild failed): %w, rollback error: %w", action, err, rbRebuildErr)
	}
	return fmt.Errorf("plugin service: failed to %s (rollback successful): %w", action, err)
}
