package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	"github.com/OnslaughtSnail/caelis/plugin/importers"
	"github.com/OnslaughtSnail/caelis/skill"
)

type Resolver struct{}

// NewResolver returns a local filesystem plugin resolver.
func NewResolver() *Resolver {
	return &Resolver{}
}

func (r *Resolver) Resolve(_ context.Context, req caelisplugin.ResolveRequest) (caelisplugin.Resolved, error) {
	root := strings.TrimSpace(req.Root)
	if root == "" {
		return caelisplugin.Resolved{}, fmt.Errorf("plugin/fs: root is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return caelisplugin.Resolved{}, err
	}
	rootAbs = filepath.Clean(rootAbs)

	manifest, err := readManifest(rootAbs)
	if err != nil {
		return caelisplugin.Resolved{}, err
	}
	if len(manifest.Contributions.Skills) == 0 {
		if dirExists(filepath.Join(rootAbs, "skills")) {
			manifest.Contributions.Skills = []caelisplugin.SkillBundle{{
				Plugin:    manifest.Name,
				Namespace: manifest.Name,
				Root:      "./skills",
			}}
		}
	}
	manifest = normalizeManifest(rootAbs, manifest)

	resolved := caelisplugin.Resolved{
		Manifest:   manifest,
		Root:       rootAbs,
		MCPServers: append([]caelisplugin.MCPServer(nil), manifest.Contributions.MCPServers...),
		Runtime: caelisplugin.RuntimeContributions{
			MCPServers:      append([]caelisplugin.MCPServer(nil), manifest.Contributions.MCPServers...),
			Agents:          append([]caelisplugin.AgentConfig(nil), manifest.Contributions.Agents...),
			Modes:           append([]caelisplugin.ModeConfig(nil), manifest.Contributions.Modes...),
			Configs:         append([]caelisplugin.ConfigOption(nil), manifest.Contributions.Configs...),
			SystemPrompt:    strings.TrimSpace(manifest.Contributions.SystemPrompt),
			PolicyMode:      strings.TrimSpace(manifest.Contributions.PolicyMode),
			ExtraReadRoots:  trimStrings(manifest.Contributions.ExtraReadRoots),
			ExtraWriteRoots: trimStrings(manifest.Contributions.ExtraWriteRoots),
		},
	}
	for _, bundle := range manifest.Contributions.Skills {
		if len(bundle.Disabled) > 0 && strings.TrimSpace(bundle.Root) == "" {
			continue
		}
		skillRoot, err := resolveContributionRoot(rootAbs, bundle.Root)
		if err != nil {
			return caelisplugin.Resolved{}, err
		}
		discovered, err := skill.Discover([]string{skillRoot})
		if err != nil {
			return caelisplugin.Resolved{}, err
		}
		disabled := disabledSet(bundle.Disabled)
		for _, one := range discovered {
			if disabled[strings.ToLower(strings.TrimSpace(one.Name))] {
				continue
			}
			if one.Metadata == nil {
				one.Metadata = map[string]any{}
			}
			one.Metadata["plugin"] = bundle.Plugin
			one.Metadata["namespace"] = bundle.Namespace
			resolved.Skills = append(resolved.Skills, one)
		}
	}
	resolved.Runtime.Skills = append([]skill.Bundle(nil), resolved.Skills...)
	return resolved, nil
}

func readManifest(root string) (caelisplugin.Manifest, error) {
	candidates := []struct {
		path string
		read func([]byte) (caelisplugin.Manifest, error)
	}{
		{path: filepath.Join(root, "caelis.plugin.json"), read: readCaelisManifest},
		{path: filepath.Join(root, ".caelis-plugin", "plugin.json"), read: readCaelisManifest},
		{path: filepath.Join(root, ".codex-plugin", "plugin.json"), read: importers.Codex},
		{path: filepath.Join(root, ".claude-plugin", "plugin.json"), read: importers.Claude},
		{path: filepath.Join(root, ".cursor-plugin", "plugin.json"), read: importers.Cursor},
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return caelisplugin.Manifest{}, err
		}
		return candidate.read(data)
	}
	name := strings.TrimSpace(filepath.Base(root))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "plugin"
	}
	return caelisplugin.Manifest{
		SchemaVersion: "caelis.plugin/v1",
		Name:          name,
	}, nil
}

func readCaelisManifest(data []byte) (caelisplugin.Manifest, error) {
	var manifest caelisplugin.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return caelisplugin.Manifest{}, err
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return caelisplugin.Manifest{}, fmt.Errorf("plugin/fs: manifest name is required")
	}
	if manifest.SchemaVersion == "" {
		manifest.SchemaVersion = "caelis.plugin/v1"
	}
	return manifest, nil
}

func normalizeManifest(root string, manifest caelisplugin.Manifest) caelisplugin.Manifest {
	manifest.Name = strings.TrimSpace(manifest.Name)
	if manifest.Name == "" {
		manifest.Name = filepath.Base(root)
	}
	for i, bundle := range manifest.Contributions.Skills {
		bundle.Plugin = strings.TrimSpace(bundle.Plugin)
		if bundle.Plugin == "" {
			bundle.Plugin = manifest.Name
		}
		bundle.Namespace = strings.TrimSpace(bundle.Namespace)
		if bundle.Namespace == "" {
			bundle.Namespace = bundle.Plugin
		}
		bundle.Root = strings.TrimSpace(bundle.Root)
		for i, disabled := range bundle.Disabled {
			bundle.Disabled[i] = strings.TrimSpace(disabled)
		}
		manifest.Contributions.Skills[i] = bundle
	}
	return manifest
}

func resolveContributionRoot(root string, contributionRoot string) (string, error) {
	contributionRoot = strings.TrimSpace(contributionRoot)
	if contributionRoot == "" {
		return "", fmt.Errorf("plugin/fs: skill root is required")
	}
	var path string
	if filepath.IsAbs(contributionRoot) {
		path = filepath.Clean(contributionRoot)
	} else {
		path = filepath.Clean(filepath.Join(root, contributionRoot))
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("plugin/fs: skill root escapes plugin root: %s", contributionRoot)
		}
	}
	return path, nil
}

func disabledSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
			out[key] = true
		}
	}
	return out
}

func trimStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
