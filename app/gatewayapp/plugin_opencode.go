package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const openCodeUnsupportedWarning = "OpenCode JS/TS plugins cannot be executed by caelis runtime; hooks, custom tools, and TUI events are not consumed"

func applyOpenCodePluginInfo(info *PluginInfo, pCfg PluginConfig) bool {
	if info == nil || !strings.EqualFold(strings.TrimSpace(pCfg.Kind), "opencode") {
		return false
	}
	info.Status = "unsupported"
	info.Warning = openCodeUnsupportedWarning
	return true
}

type OpenCodeDiscovery struct {
	LocalPlugins []OpenCodePluginSource `json:"local_plugins"`
	NPMPackages  []OpenCodeNPMPackage   `json:"npm_packages"`
	Warnings     []string               `json:"warnings,omitempty"`
}

type OpenCodePluginSource struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type OpenCodeNPMPackage struct {
	Package string `json:"package"`
	Source  string `json:"source"`
}

type openCodeConfig struct {
	Plugin []string `json:"plugin"`
}

// DiscoverOpenCode scans a workspace for OpenCode-compatible plugin sources.
func (s PluginService) DiscoverOpenCode(workspaceDir string) (OpenCodeDiscovery, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return OpenCodeDiscovery{}, fmt.Errorf("plugin service: workspace directory is required")
	}
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return OpenCodeDiscovery{}, err
	}
	out := OpenCodeDiscovery{
		Warnings: []string{openCodeUnsupportedWarning},
	}
	for _, dir := range openCodePluginDirs(absWorkspace) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return OpenCodeDiscovery{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			lower := strings.ToLower(name)
			if !strings.HasSuffix(lower, ".js") && !strings.HasSuffix(lower, ".ts") && !strings.HasSuffix(lower, ".mjs") && !strings.HasSuffix(lower, ".cjs") {
				continue
			}
			path := filepath.Join(dir, name)
			out.LocalPlugins = append(out.LocalPlugins, OpenCodePluginSource{
				Name: strings.TrimSuffix(name, filepath.Ext(name)),
				Path: path,
				Kind: "opencode-local",
			})
		}
	}
	for _, configPath := range openCodeConfigPaths(absWorkspace) {
		pkgs, err := readOpenCodeNPMPackages(configPath)
		if err != nil {
			return OpenCodeDiscovery{}, err
		}
		for _, pkg := range pkgs {
			out.NPMPackages = append(out.NPMPackages, OpenCodeNPMPackage{
				Package: pkg,
				Source:  configPath,
			})
		}
	}
	return out, nil
}

// ImportOpenCode registers discovered local OpenCode plugin files as inactive
// caelis plugins with explicit unsupported warnings.
func (s PluginService) ImportOpenCode(ctx context.Context, workspaceDir string) ([]PluginInfo, error) {
	discovery, err := s.DiscoverOpenCode(workspaceDir)
	if err != nil {
		return nil, err
	}
	out := make([]PluginInfo, 0, len(discovery.LocalPlugins))
	for _, source := range discovery.LocalPlugins {
		info, err := s.registerOpenCodeLocalPlugin(ctx, source)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	for _, pkg := range discovery.NPMPackages {
		out = append(out, PluginInfo{
			Name:    pkg.Package,
			Status:  "unsupported",
			Warning: fmt.Sprintf("%s; npm package %q listed in %s cannot be loaded", openCodeUnsupportedWarning, pkg.Package, pkg.Source),
		})
	}
	return out, nil
}

func (s PluginService) registerOpenCodeLocalPlugin(ctx context.Context, source OpenCodePluginSource) (PluginInfo, error) {
	pluginDir := filepath.Dir(source.Path)
	id := safePluginCacheName(source.Name)
	if id == "" {
		return PluginInfo{}, fmt.Errorf("plugin service: opencode plugin name is required")
	}

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("import opencode plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	entry := PluginConfig{
		ID:          "opencode-" + id,
		Name:        source.Name,
		Root:        pluginDir,
		Manifest:    source.Path,
		Kind:        "opencode",
		Enabled:     false,
		Description: "OpenCode local plugin (unsupported runtime)",
	}
	replaced := false
	for i, pCfg := range doc.Plugins {
		if pCfg.ID == entry.ID {
			doc.Plugins[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Plugins = append(doc.Plugins, entry)
	}
	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	return PluginInfo{
		ID:      entry.ID,
		Name:    entry.Name,
		Root:    entry.Root,
		Enabled: false,
		Status:  "unsupported",
		Warning: openCodeUnsupportedWarning,
	}, nil
}

func openCodePluginDirs(workspace string) []string {
	return []string{
		filepath.Join(workspace, ".opencode", "plugins"),
	}
}

func openCodeConfigPaths(workspace string) []string {
	home, _ := os.UserHomeDir()
	paths := []string{filepath.Join(workspace, "opencode.json")}
	if strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".config", "opencode", "opencode.json"))
	}
	return paths
}

func readOpenCodeNPMPackages(configPath string) ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg openCodeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("plugin service: decode opencode config %s: %w", configPath, err)
	}
	out := make([]string, 0, len(cfg.Plugin))
	seen := map[string]struct{}{}
	for _, pkg := range cfg.Plugin {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		if _, ok := seen[pkg]; ok {
			continue
		}
		seen[pkg] = struct{}{}
		out = append(out, pkg)
	}
	return out, nil
}
