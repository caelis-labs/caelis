package gatewayapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/pluginregistry"
	pluginapi "github.com/caelis-labs/caelis/ports/plugin"
)

func pluginConfigID(absPath string, override string) string {
	id := strings.TrimSpace(override)
	if id == "" {
		id = filepath.Base(absPath)
	}
	return strings.ToLower(strings.TrimSpace(id))
}

func parseConfiguredPlugin(pCfg PluginConfig) (pluginapi.InstalledPlugin, error) {
	p, err := pluginregistry.ParsePlugin(pCfg.Root)
	if err != nil {
		return pluginapi.InstalledPlugin{}, err
	}
	return pluginWithConfiguredID(p, pCfg.ID), nil
}

func upsertLocalPluginConfig(doc *AppConfig, next PluginConfig) {
	if doc == nil {
		return
	}
	if idx := pluginConfigIndexByID(doc.Plugins, next.ID); idx >= 0 {
		doc.Plugins[idx] = next
		return
	}
	doc.Plugins = append(doc.Plugins, next)
}

func upsertMarketplacePluginConfig(doc *AppConfig, next PluginConfig) error {
	if doc == nil {
		return nil
	}
	rootIdx := pluginConfigIndexByRoot(doc.Plugins, next.Root)
	if rootIdx >= 0 {
		if idIdx := pluginConfigIndexByID(doc.Plugins, next.ID); idIdx >= 0 && idIdx != rootIdx {
			return fmt.Errorf("plugin service: plugin id %q already exists at %s; cannot rename marketplace plugin at %s", strings.TrimSpace(next.ID), doc.Plugins[idIdx].Root, next.Root)
		}
		doc.Plugins[rootIdx] = next
		return nil
	}
	if idIdx := pluginConfigIndexByID(doc.Plugins, next.ID); idIdx >= 0 {
		doc.Plugins[idIdx] = next
		return nil
	}
	doc.Plugins = append(doc.Plugins, next)
	return nil
}

func pluginConfigIndexByID(configs []PluginConfig, id string) int {
	id = strings.TrimSpace(id)
	for i, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.ID), id) {
			return i
		}
	}
	return -1
}

func pluginConfigIndexByRoot(configs []PluginConfig, root string) int {
	for i, cfg := range configs {
		if samePluginRoot(cfg.Root, root) {
			return i
		}
	}
	return -1
}

func samePluginRoot(a string, b string) bool {
	aRoot, ok := canonicalPluginRoot(a)
	if !ok {
		return false
	}
	bRoot, ok := canonicalPluginRoot(b)
	if !ok {
		return false
	}
	return aRoot == bRoot
}

func canonicalPluginRoot(root string) (string, bool) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else if !os.IsNotExist(err) {
		return "", false
	}
	return filepath.Clean(abs), true
}

func pluginWithConfiguredID(p pluginapi.InstalledPlugin, id string) pluginapi.InstalledPlugin {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return p
	}
	oldID := strings.TrimSpace(p.ID)
	p.ID = id
	for i := range p.Skills {
		namespace := strings.TrimSpace(p.Skills[i].Namespace)
		if namespace == "" || strings.EqualFold(namespace, oldID) {
			p.Skills[i].Namespace = id
			p.Skills[i].Disabled = rewritePluginDisabledSkillRefs(p.Skills[i].Disabled, oldID, id)
		}
	}
	for i := range p.Hooks {
		p.Hooks[i].PluginID = id
	}
	for i := range p.MCPServers {
		p.MCPServers[i].PluginID = id
	}
	return p
}

func rewritePluginDisabledSkillRefs(disabled []string, oldID string, newID string) []string {
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" || len(disabled) == 0 {
		return disabled
	}
	out := append([]string(nil), disabled...)
	for i, value := range out {
		prefix, localName, ok := strings.Cut(strings.TrimSpace(value), ":")
		if ok && strings.EqualFold(strings.TrimSpace(prefix), oldID) {
			out[i] = newID + ":" + strings.TrimSpace(localName)
		}
	}
	return out
}
