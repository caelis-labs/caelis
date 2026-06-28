package fs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/skill"
)

// DiscoverPluginBundleMeta discovers only plugin-managed skill bundles.
func DiscoverPluginBundleMeta(bundles []skill.PluginBundle) ([]Meta, error) {
	metas, _, err := discoverPluginBundleMeta(bundles)
	return metas, err
}

func discoverPluginBundleMeta(bundles []skill.PluginBundle) ([]Meta, map[string]bool, error) {
	bundles = mergePluginBundles(bundles)
	suppressedRegular := map[string]bool{}
	if len(bundles) == 0 {
		return nil, suppressedRegular, nil
	}
	out := make([]Meta, 0)
	seenPaths := map[string]struct{}{}
	seenNames := map[string]struct{}{}
	for _, bundle := range bundles {
		resolvedDir, err := ResolvePath(bundle.Root)
		if err != nil {
			if bundle.Enabled {
				return nil, nil, err
			}
			continue
		}
		info, err := os.Stat(resolvedDir)
		if err != nil {
			if bundle.Enabled && !os.IsNotExist(err) {
				return nil, nil, err
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(resolvedDir)
		if err != nil {
			if bundle.Enabled {
				return nil, nil, err
			}
			continue
		}
		for _, entry := range entries {
			if entry == nil || !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(resolvedDir, entry.Name(), "SKILL.md")
			info, err := os.Stat(skillPath)
			if err != nil {
				if bundle.Enabled && !os.IsNotExist(err) {
					return nil, nil, err
				}
				continue
			}
			skillPath = filepath.Clean(skillPath)
			meta, hash, err := parseMetaHashCached(skillPath, info)
			if err != nil {
				if bundle.Enabled {
					return nil, nil, err
				}
				continue
			}
			localName := strings.TrimSpace(meta.Name)
			if localName == "" {
				continue
			}
			suppressedRegular[skillIdentityKey(localName, hash)] = true
			if !bundle.Enabled {
				continue
			}
			namespace := pluginBundleNamespace(bundle)
			if namespace == "" {
				continue
			}
			namespacedName := namespace + ":" + localName
			if pluginBundleDisables(bundle, localName, namespacedName) {
				continue
			}
			nameKey := strings.ToLower(strings.TrimSpace(namespacedName))
			if nameKey == "" {
				continue
			}
			identityPath := strings.ToLower(pluginBundlePlugin(bundle)) + "\x00" + skillPath
			if _, ok := seenPaths[identityPath]; ok {
				continue
			}
			seenPaths[identityPath] = struct{}{}
			if _, ok := seenNames[nameKey]; ok {
				continue
			}
			seenNames[nameKey] = struct{}{}
			meta.Name = namespacedName
			meta.Source = skill.SourcePlugin
			meta.PluginID = pluginBundlePlugin(bundle)
			meta.Namespace = namespace
			meta.LocalName = localName
			out = append(out, meta)
		}
	}
	return out, suppressedRegular, nil
}

func mergePluginBundles(in []skill.PluginBundle) []skill.PluginBundle {
	if len(in) == 0 {
		return nil
	}
	out := make([]skill.PluginBundle, 0, len(in))
	seen := map[string]int{}
	for _, bundle := range in {
		root := strings.TrimSpace(bundle.Root)
		if root == "" {
			continue
		}
		key := strings.ToLower(strings.Join([]string{
			pluginBundlePlugin(bundle),
			pluginBundleNamespace(bundle),
			filepath.Clean(root),
		}, "\x00"))
		if idx, ok := seen[key]; ok {
			out[idx].Enabled = out[idx].Enabled || bundle.Enabled
			out[idx].Disabled = append(out[idx].Disabled, bundle.Disabled...)
			continue
		}
		seen[key] = len(out)
		bundle.Disabled = append([]string(nil), bundle.Disabled...)
		out = append(out, bundle)
	}
	return out
}

func pluginBundlePlugin(bundle skill.PluginBundle) string {
	return strings.TrimSpace(bundle.Plugin)
}

func pluginBundleNamespace(bundle skill.PluginBundle) string {
	namespace := strings.TrimSpace(bundle.Namespace)
	if namespace == "" {
		namespace = pluginBundlePlugin(bundle)
	}
	return strings.TrimSpace(namespace)
}

func pluginBundleDisables(bundle skill.PluginBundle, localName string, namespacedName string) bool {
	localKey := strings.ToLower(strings.TrimSpace(localName))
	namespacedKey := strings.ToLower(strings.TrimSpace(namespacedName))
	for _, disabled := range bundle.Disabled {
		key := strings.ToLower(strings.TrimSpace(disabled))
		if key != "" && (key == localKey || key == namespacedKey) {
			return true
		}
	}
	return false
}

func skillIdentityKey(name string, hash string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "\x00" + strings.TrimSpace(hash)
}
