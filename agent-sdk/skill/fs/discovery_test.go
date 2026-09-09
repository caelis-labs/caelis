package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
)

func TestLoaderLoadUsesProvidedPathWithoutRediscoveringDirectory(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	bad := filepath.Join(root, "bad")
	for _, dir := range []string{good, bad} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: good\ndescription: Good skill.\n---\n# Good\n\nUse this skill.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(good SKILL.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile(bad SKILL.md) error = %v", err)
	}

	bundle, err := (Loader{}).Load(context.Background(), skill.Ref{
		Name:      "namespace:good",
		Path:      filepath.Join(good, "SKILL.md"),
		Namespace: "namespace",
		LocalName: "good",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if bundle.Meta.Name != "namespace:good" {
		t.Fatalf("bundle.Meta.Name = %q, want canonical ref name", bundle.Meta.Name)
	}
	if bundle.Meta.LocalName != "good" {
		t.Fatalf("bundle.Meta.LocalName = %q, want parsed local name", bundle.Meta.LocalName)
	}
	if !strings.Contains(bundle.Content, "Use this skill.") {
		t.Fatalf("bundle.Content = %q, want skill content", bundle.Content)
	}
	if strings.Contains(bundle.Content, "---") || strings.Contains(bundle.Content, "description:") {
		t.Fatalf("bundle.Content = %q, should not include front matter", bundle.Content)
	}
}

func TestLoaderRejectsMismatchedNamespacedRef(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o700); err != nil {
		t.Fatalf("MkdirAll(good) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: good\ndescription: Good skill.\n---\n# Good\n\nUse this skill.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(good SKILL.md) error = %v", err)
	}

	_, err := (Loader{}).Load(context.Background(), skill.Ref{
		Name:      "other:good",
		Path:      filepath.Join(good, "SKILL.md"),
		Namespace: "namespace",
		LocalName: "good",
	})
	if err == nil {
		t.Fatal("Load() error = nil, want namespace mismatch rejection")
	}
}

func TestDiscoverMetaIncludesPluginSkillsNamespacedAndSuppressesLegacyCopy(t *testing.T) {
	root := t.TempDir()
	regularRoot := filepath.Join(root, "regular")
	pluginRoot := filepath.Join(root, "plugin-skills")

	writeSkillForDiscoveryTest(t, filepath.Join(regularRoot, "ordinary"), "ordinary", "ordinary skill")
	writeSkillForDiscoveryTest(t, filepath.Join(pluginRoot, "plan"), "plan", "plugin plan skill")
	pluginSkillData, err := os.ReadFile(filepath.Join(pluginRoot, "plan", "SKILL.md"))
	if err != nil {
		t.Fatalf("read plugin skill: %v", err)
	}
	legacyCopy := filepath.Join(regularRoot, "plan")
	if err := os.MkdirAll(legacyCopy, 0o755); err != nil {
		t.Fatalf("mkdir legacy copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyCopy, "SKILL.md"), pluginSkillData, 0o600); err != nil {
		t.Fatalf("write legacy copy: %v", err)
	}

	metas, err := DiscoverMetaRequest(skill.DiscoverRequest{
		Dirs: []string{regularRoot},
		PluginBundles: []skill.PluginBundle{{
			Plugin:    "superpowers",
			Namespace: "superpowers",
			Root:      pluginRoot,
			Enabled:   true,
		}},
	})
	if err != nil {
		t.Fatalf("DiscoverMetaRequest() error = %v", err)
	}
	if metaByNameForDiscoveryTest(metas, "ordinary").Source != skill.SourceRegular {
		t.Fatalf("ordinary skill missing from %#v", metas)
	}
	if got := metaByNameForDiscoveryTest(metas, "superpowers:plan"); got.Source != skill.SourcePlugin || got.LocalName != "plan" || got.PluginID != "superpowers" {
		t.Fatalf("plugin skill meta = %#v, want namespaced plugin meta", got)
	}
	if got := metaByNameForDiscoveryTest(metas, "plan"); got.Name != "" {
		t.Fatalf("legacy regular plugin copy leaked as %#v", got)
	}

	copies, err := DiscoverLegacyPluginCopies(skill.DiscoverRequest{
		Dirs: []string{regularRoot},
		PluginBundles: []skill.PluginBundle{{
			Plugin:    "superpowers",
			Namespace: "superpowers",
			Root:      pluginRoot,
			Enabled:   true,
		}},
	})
	if err != nil {
		t.Fatalf("DiscoverLegacyPluginCopies() error = %v", err)
	}
	if len(copies) != 1 || copies[0].Path != filepath.Join(legacyCopy, "SKILL.md") {
		t.Fatalf("legacy copies = %#v, want copied plan skill", copies)
	}
}

func TestDiscoverMetaSuppressesDisabledPluginSkillCopies(t *testing.T) {
	root := t.TempDir()
	regularRoot := filepath.Join(root, "regular")
	pluginRoot := filepath.Join(root, "plugin-skills")

	writeSkillForDiscoveryTest(t, filepath.Join(pluginRoot, "tooling"), "tooling", "plugin tooling skill")
	pluginSkillData, err := os.ReadFile(filepath.Join(pluginRoot, "tooling", "SKILL.md"))
	if err != nil {
		t.Fatalf("read plugin skill: %v", err)
	}
	legacyCopy := filepath.Join(regularRoot, "tooling")
	if err := os.MkdirAll(legacyCopy, 0o755); err != nil {
		t.Fatalf("mkdir legacy copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyCopy, "SKILL.md"), pluginSkillData, 0o600); err != nil {
		t.Fatalf("write legacy copy: %v", err)
	}

	metas, err := DiscoverMetaRequest(skill.DiscoverRequest{
		Dirs: []string{regularRoot},
		PluginBundles: []skill.PluginBundle{{
			Plugin:    "disabled-plugin",
			Namespace: "disabled-plugin",
			Root:      pluginRoot,
			Enabled:   false,
		}},
	})
	if err != nil {
		t.Fatalf("DiscoverMetaRequest() error = %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("metas = %#v, want disabled plugin skill and legacy copy hidden", metas)
	}
}

func TestParseMetaCacheIsBounded(t *testing.T) {
	metaCache.Lock()
	previousNext := metaCache.next
	previousEntries := metaCache.entries
	metaCache.next = 0
	metaCache.entries = map[string]metaCacheEntry{}
	metaCache.Unlock()
	t.Cleanup(func() {
		metaCache.Lock()
		metaCache.next = previousNext
		metaCache.entries = previousEntries
		metaCache.Unlock()
	})

	// Seed the full cache directly; only misses crossing the capacity boundary
	// need real files and parsing. Keep deterministic recency for eviction checks.
	metaCache.Lock()
	for i := range maxMetaCacheEntries {
		metaCache.entries[fmt.Sprintf("cached-%03d", i)] = metaCacheEntry{used: uint64(i + 1)}
	}
	metaCache.next = maxMetaCacheEntries
	metaCache.Unlock()
	root := t.TempDir()
	for i := range 2 {
		name := fmt.Sprintf("new-%d", i)
		dir := filepath.Join(root, name)
		writeSkillForDiscoveryTest(t, dir, name, "cache bounded")
		path := filepath.Join(dir, "SKILL.md")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := parseMetaCached(path, info)
		if err != nil || meta.Name != name {
			t.Fatalf("parseMetaCached(%s) = %#v, %v", path, meta, err)
		}
	}
	metaCache.Lock()
	got := len(metaCache.entries)
	_, oldestRetained := metaCache.entries["cached-000"]
	_, secondOldestRetained := metaCache.entries["cached-001"]
	_, newestRetained := metaCache.entries[fmt.Sprintf("cached-%03d", maxMetaCacheEntries-1)]
	metaCache.Unlock()
	if got != maxMetaCacheEntries || oldestRetained || secondOldestRetained || !newestRetained {
		t.Fatalf("cache entries=%d, oldest retained=%v/%v, newest retained=%v", got, oldestRetained, secondOldestRetained, newestRetained)
	}
}

func writeSkillForDiscoveryTest(t *testing.T, dir string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill %s: %v", dir, err)
	}
}

func metaByNameForDiscoveryTest(metas []Meta, name string) Meta {
	for _, meta := range metas {
		if meta.Name == name {
			return meta
		}
	}
	return Meta{}
}
