package mcpconfig

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Request is the assembly-time input for one Runtime's MCP catalog.
type Request struct {
	Native               Servers
	UserAgentsFile       string
	ProjectAgentsFile    string
	ProjectMCPFile       string
	ProjectRoot          string
	AllowProjectOverlays bool
}

type overlayEntry struct {
	cfg      ServerConfig
	source   string
	fileDir  string
	baseRoot string
}

// Resolve loads compatibility overlay files, overlays the native catalog, and
// returns enabled ServerSpec values. Missing overlay files are skipped. The
// result does not include plugin-contributed servers.
func Resolve(req Request) ([]sdkmcp.ServerSpec, error) {
	merged := map[string]overlayEntry{}
	if err := overlayFile(merged, req.UserAgentsFile, NamespaceUser, ""); err != nil {
		return nil, err
	}
	if err := overlayNative(merged, req.Native, req.ProjectRoot); err != nil {
		return nil, err
	}
	if req.AllowProjectOverlays {
		if err := overlayFile(merged, req.ProjectAgentsFile, NamespaceProject, req.ProjectRoot); err != nil {
			return nil, err
		}
		if err := overlayFile(merged, req.ProjectMCPFile, NamespaceProject, req.ProjectRoot); err != nil {
			return nil, err
		}
	}

	out := make([]sdkmcp.ServerSpec, 0, len(merged))
	for _, name := range slices.Sorted(maps.Keys(merged)) {
		entry := merged[name]
		if !entry.cfg.IsEnabled() {
			continue
		}
		if err := validateEnabledServer(name, entry.cfg); err != nil {
			return nil, err
		}
		spec, err := specFromEntry(name, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func overlayNative(dst map[string]overlayEntry, native Servers, projectRoot string) error {
	native = Normalize(native)
	for _, name := range slices.Sorted(maps.Keys(native)) {
		dst[name] = overlayEntry{
			cfg:      native[name],
			source:   NamespaceUser,
			baseRoot: strings.TrimSpace(projectRoot),
		}
	}
	return nil
}

func overlayFile(dst map[string]overlayEntry, path, source, projectRoot string) error {
	servers, err := loadOverlayFile(path)
	if err != nil {
		return err
	}
	fileDir := ""
	if strings.TrimSpace(path) != "" {
		fileDir = filepath.Dir(path)
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		dst[name] = overlayEntry{
			cfg:      servers[name],
			source:   source,
			fileDir:  fileDir,
			baseRoot: strings.TrimSpace(projectRoot),
		}
	}
	return nil
}

func specFromEntry(name string, entry overlayEntry) (sdkmcp.ServerSpec, error) {
	transport := sdkmcp.NormalizeTransport(firstNonEmpty(entry.cfg.Transport, entry.cfg.Type), entry.cfg.Command, entry.cfg.URL)
	workDir, err := resolveWorkDir(entry, transport == sdkmcp.TransportStdio)
	if err != nil {
		return sdkmcp.ServerSpec{}, fmt.Errorf("mcpconfig: MCP server %q: %w", name, err)
	}
	return sdkmcp.ServerSpec{
		PluginID:  entry.source,
		Name:      name,
		Transport: transport,
		Command:   entry.cfg.Command,
		Args:      append([]string(nil), entry.cfg.Args...),
		Env:       cloneStringMap(entry.cfg.Env),
		WorkDir:   workDir,
		URL:       entry.cfg.URL,
		Headers:   cloneStringMap(entry.cfg.Headers),
	}, nil
}

func resolveWorkDir(entry overlayEntry, stdio bool) (string, error) {
	workDir := strings.TrimSpace(entry.cfg.WorkDir)
	if workDir == "" {
		if !stdio {
			return "", nil
		}
		return defaultWorkDir(entry)
	}
	if filepath.IsAbs(workDir) {
		if err := checkProjectBound(entry.source, entry.baseRoot, workDir); err != nil {
			return "", err
		}
		return filepath.Clean(workDir), nil
	}
	base := strings.TrimSpace(entry.baseRoot)
	if entry.source != NamespaceProject && strings.TrimSpace(entry.fileDir) != "" {
		base = entry.fileDir
	}
	if base == "" {
		return "", fmt.Errorf("relative workDir %q needs a base directory", workDir)
	}
	joined := filepath.Clean(filepath.Join(base, workDir))
	if err := checkProjectBound(entry.source, entry.baseRoot, joined); err != nil {
		return "", err
	}
	return joined, nil
}

func defaultWorkDir(entry overlayEntry) (string, error) {
	if entry.source == NamespaceProject && strings.TrimSpace(entry.baseRoot) != "" {
		return filepath.Clean(entry.baseRoot), nil
	}
	if strings.TrimSpace(entry.fileDir) != "" {
		return filepath.Clean(entry.fileDir), nil
	}
	if strings.TrimSpace(entry.baseRoot) != "" {
		return filepath.Clean(entry.baseRoot), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("stdio server requires workDir")
	}
	return cwd, nil
}

func checkProjectBound(source, projectRoot, target string) error {
	if source != NamespaceProject {
		return nil
	}
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return nil
	}
	if !pathWithinRoot(root, target) {
		return fmt.Errorf("workDir %q escapes project root", target)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project root %q: %w", root, err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve project workDir %q: %w", target, err)
	}
	if !pathWithinRoot(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("workDir %q escapes project root", target)
	}
	return nil
}

func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// CombineSpecs concatenates catalog overlay specs and plugin MCP specs.
// Reserved overlay namespaces cannot be reused as plugin IDs, and
// (pluginID, name) pairs must be unique before any client is started.
func CombineSpecs(catalog []sdkmcp.ServerSpec, pluginSpecs []sdkmcp.ServerSpec) ([]sdkmcp.ServerSpec, error) {
	if len(catalog) == 0 && len(pluginSpecs) == 0 {
		return nil, nil
	}
	out := make([]sdkmcp.ServerSpec, 0, len(catalog)+len(pluginSpecs))
	seen := make(map[string]struct{}, len(catalog)+len(pluginSpecs))
	appendSpec := func(spec sdkmcp.ServerSpec) error {
		key := spec.PluginID + "\x00" + spec.Name
		if _, exists := seen[key]; exists {
			return fmt.Errorf("mcpconfig: duplicate MCP server %s/%s", spec.PluginID, spec.Name)
		}
		seen[key] = struct{}{}
		out = append(out, spec)
		return nil
	}
	for _, spec := range catalog {
		if err := appendSpec(spec); err != nil {
			return nil, err
		}
	}
	for _, spec := range pluginSpecs {
		if ReservedNamespace(spec.PluginID) {
			return nil, fmt.Errorf("mcpconfig: plugin %q uses reserved MCP namespace", spec.PluginID)
		}
		if err := appendSpec(spec); err != nil {
			return nil, err
		}
	}
	return out, nil
}
