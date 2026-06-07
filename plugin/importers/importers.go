package importers

import (
	"encoding/json"
	"fmt"
	"strings"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
)

type externalManifest struct {
	Name          string                         `json:"name"`
	DisplayName   string                         `json:"displayName"`
	Version       string                         `json:"version"`
	Description   string                         `json:"description"`
	Author        caelisplugin.Author            `json:"author"`
	Homepage      string                         `json:"homepage"`
	Repository    string                         `json:"repository"`
	License       string                         `json:"license"`
	Keywords      []string                       `json:"keywords"`
	Skills        any                            `json:"skills"`
	Agents        any                            `json:"agents"`
	Commands      string                         `json:"commands"`
	Hooks         string                         `json:"hooks"`
	MCPServers    []caelisplugin.MCPServer       `json:"mcpServers"`
	Interface     caelisplugin.InterfaceMetadata `json:"interface"`
	Contributions caelisplugin.Contributions     `json:"contributions"`
}

// Codex normalizes a .codex-plugin/plugin.json manifest.
func Codex(data []byte) (caelisplugin.Manifest, error) {
	return normalize(data)
}

// Claude normalizes a .claude-plugin/plugin.json manifest.
func Claude(data []byte) (caelisplugin.Manifest, error) {
	return normalize(data)
}

// Cursor normalizes a .cursor-plugin/plugin.json manifest.
func Cursor(data []byte) (caelisplugin.Manifest, error) {
	return normalize(data)
}

func normalize(data []byte) (caelisplugin.Manifest, error) {
	var raw externalManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return caelisplugin.Manifest{}, err
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return caelisplugin.Manifest{}, fmt.Errorf("plugin importer: name is required")
	}
	manifest := caelisplugin.Manifest{
		SchemaVersion: "caelis.plugin/v1",
		Name:          name,
		Version:       strings.TrimSpace(raw.Version),
		Description:   strings.TrimSpace(raw.Description),
		Author:        raw.Author,
		Homepage:      strings.TrimSpace(raw.Homepage),
		Repository:    strings.TrimSpace(raw.Repository),
		License:       strings.TrimSpace(raw.License),
		Keywords:      append([]string(nil), raw.Keywords...),
		Contributions: raw.Contributions,
		Interface:     raw.Interface,
	}
	if manifest.Interface.DisplayName == "" {
		manifest.Interface.DisplayName = strings.TrimSpace(raw.DisplayName)
	}
	manifest.Contributions.Skills = append(manifest.Contributions.Skills, skillBundles(name, raw.Skills)...)
	manifest.Contributions.MCPServers = append(manifest.Contributions.MCPServers, raw.MCPServers...)
	return normalizeManifest(manifest), nil
}

func skillBundles(pluginName string, raw any) []caelisplugin.SkillBundle {
	switch typed := raw.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []caelisplugin.SkillBundle{{
			Plugin:    pluginName,
			Namespace: pluginName,
			Root:      strings.TrimSpace(typed),
		}}
	case []any:
		out := make([]caelisplugin.SkillBundle, 0, len(typed))
		for _, one := range typed {
			switch value := one.(type) {
			case string:
				out = append(out, skillBundles(pluginName, value)...)
			case map[string]any:
				root, _ := value["root"].(string)
				if strings.TrimSpace(root) == "" {
					continue
				}
				namespace, _ := value["namespace"].(string)
				if strings.TrimSpace(namespace) == "" {
					namespace = pluginName
				}
				out = append(out, caelisplugin.SkillBundle{
					Plugin:    pluginName,
					Namespace: strings.TrimSpace(namespace),
					Root:      strings.TrimSpace(root),
				})
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeManifest(in caelisplugin.Manifest) caelisplugin.Manifest {
	for i, one := range in.Contributions.Skills {
		if strings.TrimSpace(one.Plugin) == "" {
			one.Plugin = in.Name
		}
		if strings.TrimSpace(one.Namespace) == "" {
			one.Namespace = one.Plugin
		}
		one.Plugin = strings.TrimSpace(one.Plugin)
		one.Namespace = strings.TrimSpace(one.Namespace)
		one.Root = strings.TrimSpace(one.Root)
		in.Contributions.Skills[i] = one
	}
	return in
}
