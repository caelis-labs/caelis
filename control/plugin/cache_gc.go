package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var managedPluginCacheLifecycle = struct {
	effects sync.RWMutex
	pinsMu  sync.Mutex
	pins    map[string]int
}{
	pins: make(map[string]int),
}

// beginManagedPluginMaterialization prevents cache GC from crossing the gap
// between publishing immutable plugin content and committing its Config.
func beginManagedPluginMaterialization() func() {
	managedPluginCacheLifecycle.effects.RLock()
	var once sync.Once
	return func() {
		once.Do(managedPluginCacheLifecycle.effects.RUnlock)
	}
}

// RetainManagedPluginCaches pins the immutable managed content referenced by
// one assembled Runtime until that Runtime releases its file-backed resources.
func RetainManagedPluginCaches(storeDir string, configs []Config) func() {
	managedPluginCacheLifecycle.effects.RLock()
	roots := managedPluginContentRoots(storeDir, configs)
	managedPluginCacheLifecycle.pinsMu.Lock()
	for root := range roots {
		managedPluginCacheLifecycle.pins[root]++
	}
	managedPluginCacheLifecycle.pinsMu.Unlock()
	managedPluginCacheLifecycle.effects.RUnlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			managedPluginCacheLifecycle.pinsMu.Lock()
			defer managedPluginCacheLifecycle.pinsMu.Unlock()
			for root := range roots {
				managedPluginCacheLifecycle.pins[root]--
				if managedPluginCacheLifecycle.pins[root] <= 0 {
					delete(managedPluginCacheLifecycle.pins, root)
				}
			}
		})
	}
}

// ReclaimManagedCaches removes immutable install content referenced by neither
// current Plugin configuration nor an assembled Runtime.
func (s Service) ReclaimManagedCaches(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	managedPluginCacheLifecycle.effects.Lock()
	defer managedPluginCacheLifecycle.effects.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := s.loadState(ctx)
	if err != nil {
		return err
	}
	storeDir, err := s.storeDirectory()
	if err != nil {
		return err
	}
	return reclaimManagedPluginCaches(ctx, storeDir, state.Plugins, managedPluginCachePins())
}

func managedPluginCachePins() map[string]struct{} {
	managedPluginCacheLifecycle.pinsMu.Lock()
	defer managedPluginCacheLifecycle.pinsMu.Unlock()
	out := make(map[string]struct{}, len(managedPluginCacheLifecycle.pins))
	for root, count := range managedPluginCacheLifecycle.pins {
		if count > 0 {
			out[root] = struct{}{}
		}
	}
	return out
}

func reclaimManagedPluginCaches(
	ctx context.Context,
	storeDir string,
	configs []Config,
	pins map[string]struct{},
) error {
	installedRoot, err := filepath.Abs(filepath.Join(strings.TrimSpace(storeDir), "plugins", "installed"))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(installedRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	protectedNamespaces, protectedContent := managedPluginCacheProtection(storeDir, configs, pins)
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		cacheRoot := filepath.Clean(filepath.Join(installedRoot, entry.Name()))
		if !PathWithinRoot(installedRoot, cacheRoot) || cacheRoot == filepath.Clean(installedRoot) {
			errs = append(errs, fmt.Errorf("plugin service: invalid managed cache path %q", cacheRoot))
			continue
		}
		release, gateErr := acquireManagedPluginCacheOperation(ctx, cacheRoot)
		if gateErr != nil {
			errs = append(errs, gateErr)
			continue
		}
		protected := protectedContent[cacheRoot]
		switch {
		case !protectedNamespaces[cacheRoot]:
			err = os.RemoveAll(cacheRoot)
		case len(protected) == 0:
			// An invalid but currently referenced Config fails closed by keeping
			// its whole namespace until configuration is repaired.
			err = nil
		default:
			err = reclaimManagedPluginCacheChildren(cacheRoot, protected)
		}
		release()
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin service: reclaim managed cache %q: %w", cacheRoot, err))
		}
	}
	return errors.Join(errs...)
}

func reclaimManagedPluginCacheChildren(cacheRoot string, protected map[string]struct{}) error {
	entries, err := os.ReadDir(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		child := filepath.Clean(filepath.Join(cacheRoot, entry.Name()))
		if !PathWithinRoot(cacheRoot, child) || child == filepath.Clean(cacheRoot) {
			errs = append(errs, fmt.Errorf("invalid managed cache child %q", child))
			continue
		}
		if _, ok := protected[child]; ok {
			continue
		}
		if err := os.RemoveAll(child); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func managedPluginCacheProtection(
	storeDir string,
	configs []Config,
	pins map[string]struct{},
) (map[string]bool, map[string]map[string]struct{}) {
	namespaces := make(map[string]bool)
	content := make(map[string]map[string]struct{})
	for _, cfg := range configs {
		if !cfg.Managed {
			continue
		}
		cacheRoot, contentRoot, ok := managedPluginContentRoot(storeDir, cfg)
		if cacheRoot == "" {
			continue
		}
		namespaces[cacheRoot] = true
		if !ok {
			continue
		}
		if content[cacheRoot] == nil {
			content[cacheRoot] = make(map[string]struct{})
		}
		content[cacheRoot][contentRoot] = struct{}{}
	}
	for contentRoot := range pins {
		cacheRoot := filepath.Dir(contentRoot)
		namespaces[cacheRoot] = true
		if content[cacheRoot] == nil {
			content[cacheRoot] = make(map[string]struct{})
		}
		content[cacheRoot][contentRoot] = struct{}{}
	}
	return namespaces, content
}

func managedPluginContentRoots(storeDir string, configs []Config) map[string]struct{} {
	roots := make(map[string]struct{})
	for _, cfg := range configs {
		if !cfg.Managed || !cfg.Enabled {
			continue
		}
		_, contentRoot, ok := managedPluginContentRoot(storeDir, cfg)
		if ok {
			roots[contentRoot] = struct{}{}
		}
	}
	return roots
}

func managedPluginContentRoot(storeDir string, cfg Config) (string, string, bool) {
	installedRoot, err := filepath.Abs(filepath.Join(strings.TrimSpace(storeDir), "plugins", "installed"))
	if err != nil {
		return "", "", false
	}
	cacheRoot := managedPluginInstallCacheRoot(storeDir, cfg.Root)
	if cacheRoot == "" {
		return "", "", false
	}
	cacheRoot = filepath.Clean(cacheRoot)
	if configured := strings.TrimSpace(cfg.CacheRoot); configured != "" {
		configured, configuredErr := filepath.Abs(configured)
		if configuredErr != nil || filepath.Clean(configured) != cacheRoot {
			return cacheRoot, "", false
		}
	}
	if !PathWithinRoot(installedRoot, cacheRoot) || filepath.Dir(cacheRoot) != filepath.Clean(installedRoot) {
		return cacheRoot, "", false
	}
	pluginRoot, err := filepath.Abs(strings.TrimSpace(cfg.Root))
	if err != nil || !PathWithinRoot(cacheRoot, pluginRoot) || pluginRoot == filepath.Clean(cacheRoot) {
		return cacheRoot, "", false
	}
	rel, err := filepath.Rel(cacheRoot, pluginRoot)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return cacheRoot, "", false
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." || parts[0] == ".." || strings.TrimSpace(parts[0]) == "" {
		return cacheRoot, "", false
	}
	contentRoot := filepath.Clean(filepath.Join(cacheRoot, parts[0]))
	if !PathWithinRoot(cacheRoot, contentRoot) || filepath.Dir(contentRoot) != filepath.Clean(cacheRoot) {
		return cacheRoot, "", false
	}
	return cacheRoot, contentRoot, true
}
