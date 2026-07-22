package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

func pluginConfigID(absPath string, override string) string {
	id := strings.TrimSpace(override)
	if id == "" {
		id = filepath.Base(absPath)
	}
	return strings.ToLower(strings.TrimSpace(id))
}

// ParseConfigured parses a plugin directory and applies its persisted Control ID
// to namespaced contributions.
func ParseConfigured(pCfg Config) (InstalledPlugin, error) {
	p, err := ParsePlugin(pCfg.Root)
	if err != nil {
		return InstalledPlugin{}, err
	}
	return pluginWithConfiguredID(p, pCfg.ID), nil
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

func pluginWithConfiguredID(p InstalledPlugin, id string) InstalledPlugin {
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
