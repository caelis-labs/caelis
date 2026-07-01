package pluginregistry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/plugin"
)

type CaelisPluginJSON struct {
	Name        string                         `json:"name"`
	Version     string                         `json:"version"`
	Description string                         `json:"description"`
	Skills      []CaelisSkillContribution      `json:"skills"`
	Hooks       map[string][]CaelisHookSpec    `json:"hooks"`
	MCPServers  map[string]CaelisMCPServerSpec `json:"mcpServers"`
	Agents      []CaelisAgentContribution      `json:"agents"`
}

type CaelisSkillContribution struct {
	Root      string   `json:"root"`
	Namespace string   `json:"namespace"`
	Disabled  []string `json:"disabled"`
}

type CaelisHookSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	WorkDir string            `json:"workDir"`
	Timeout string            `json:"timeout"`
}

type CaelisMCPServerSpec struct {
	Transport string            `json:"transport"`
	Type      string            `json:"type"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	WorkDir   string            `json:"workDir"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

type CaelisAgentContribution struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	WorkDir     string            `json:"workDir"`
}

type ClaudePluginJSON struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type ClaudeHooksJSON struct {
	Hooks map[string][]ClaudeHookGroup `json:"hooks"`
}

type ClaudeHookGroup struct {
	Matcher string       `json:"matcher"`
	Hooks   []ClaudeHook `json:"hooks"`
}

type ClaudeHook struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func ResolveSafePath(pluginRoot, relativePath string) (string, error) {
	cleanRoot := filepath.Clean(pluginRoot)
	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		realRoot = cleanRoot
	} else {
		realRoot = filepath.Clean(realRoot)
	}

	joined := filepath.Join(cleanRoot, relativePath)
	cleanJoined := filepath.Clean(joined)

	// 1. Lexical prefix boundary check
	if !PathWithinRoot(cleanRoot, cleanJoined) {
		return "", fmt.Errorf("pluginregistry: path traversal escape detected: %s escapes %s", relativePath, pluginRoot)
	}

	// 2. Physical boundary check by resolving symlinks on the longest existing parent path
	current := cleanJoined
	var suffixParts []string
	var realPrefix string
	var displayPrefix string
	for {
		realCurrent, err := filepath.EvalSymlinks(current)
		if err == nil {
			realCurrent = filepath.Clean(realCurrent)
			if !PathWithinRoot(realRoot, realCurrent) {
				return "", fmt.Errorf("pluginregistry: path traversal escape detected (via parent symlink): %s escapes %s", relativePath, pluginRoot)
			}
			realPrefix = realCurrent
			displayPrefix = current
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffixParts = append([]string{filepath.Base(current)}, suffixParts...)
		current = parent
	}

	if realPrefix != "" {
		if pathContainsSymlink(cleanRoot, displayPrefix) {
			resolved := realPrefix
			for _, part := range suffixParts {
				resolved = filepath.Join(resolved, part)
			}
			return filepath.Clean(resolved), nil
		}
	}

	return cleanJoined, nil
}

func PathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func pathContainsSymlink(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func ParsePlugin(root string) (plugin.InstalledPlugin, error) {
	root = filepath.Clean(root)
	pluginID := strings.ToLower(filepath.Base(root))

	p := plugin.InstalledPlugin{
		ID:   pluginID,
		Root: root,
	}

	hasAnyManifest := false

	// 1. Native Caelis manifest
	caelisPath := filepath.Join(root, ".caelis-plugin", "plugin.json")
	if _, err := os.Stat(caelisPath); err == nil {
		hasAnyManifest = true
		cp, err := parseCaelisPluginRaw(root, caelisPath)
		if err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: parse caelis manifest: %w", err)
		}
		p.Kind = plugin.ManifestKindCaelis
		p.Manifest = caelisPath
		mergeInstalledPlugin(&p, cp)
	}

	// 2. Claude Code manifest
	claudePath := filepath.Join(root, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(claudePath); err == nil {
		hasAnyManifest = true
		clp, err := parseClaudePluginRaw(root, claudePath)
		if err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: parse claude manifest: %w", err)
		}
		if p.Kind == "" {
			p.Kind = plugin.ManifestKindClaude
			p.Manifest = claudePath
		}
		mergeInstalledPlugin(&p, clp)
	}

	if !hasAnyManifest {
		return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: no valid manifest found in %s", root)
	}

	// Ensure implicit skills are added if skills are empty
	if len(p.Skills) == 0 {
		addImplicitSkills(root, pluginID, &p)
	}

	return p, nil
}

func mergeInstalledPlugin(dest *plugin.InstalledPlugin, src plugin.InstalledPlugin) {
	if dest.Name == "" || dest.Name == dest.ID {
		dest.Name = src.Name
	}
	if dest.Version == "" {
		dest.Version = src.Version
	}
	if dest.Description == "" {
		dest.Description = src.Description
	}
	dest.Skills = append(dest.Skills, src.Skills...)
	dest.Hooks = append(dest.Hooks, src.Hooks...)
	dest.MCPServers = append(dest.MCPServers, src.MCPServers...)
	dest.Agents = append(dest.Agents, src.Agents...)
}

func parseCaelisPluginRaw(root, manifestPath string) (plugin.InstalledPlugin, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	var manifest CaelisPluginJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		return plugin.InstalledPlugin{}, err
	}

	pluginID := strings.ToLower(filepath.Base(root))
	p := plugin.InstalledPlugin{
		ID:          pluginID,
		Name:        firstNonEmpty(manifest.Name, pluginID),
		Version:     manifest.Version,
		Root:        root,
		Manifest:    manifestPath,
		Kind:        plugin.ManifestKindCaelis,
		Description: manifest.Description,
	}

	skills, err := skillContributionsFromObjects(root, pluginID, manifest.Skills)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	p.Skills = append(p.Skills, skills...)

	hooks, err := caelisHooksFromManifest(root, pluginID, manifest.Hooks)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	p.Hooks = append(p.Hooks, hooks...)

	mcpSpecs, err := mcpServerSpecsFromMap(root, pluginID, manifest.MCPServers)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	p.MCPServers = append(p.MCPServers, mcpSpecs...)

	agents, err := agentContributionsFromManifest(root, manifest.Agents)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	p.Agents = append(p.Agents, agents...)

	return p, nil
}

func buildMCPServerSpec(root, pluginID, name string, mcp CaelisMCPServerSpec) (plugin.MCPServerSpec, error) {
	transport := plugin.NormalizeMCPTransport(firstNonEmpty(mcp.Transport, mcp.Type), mcp.Command, mcp.URL)
	resolvedWorkDir := ""
	if mcp.WorkDir != "" {
		var err error
		resolvedWorkDir, err = ResolveSafePath(root, mcp.WorkDir)
		if err != nil {
			return plugin.MCPServerSpec{}, fmt.Errorf("pluginregistry: invalid workDir %q: %w", mcp.WorkDir, err)
		}
	} else if transport == plugin.MCPTransportStdio {
		resolvedWorkDir = root
	}
	return plugin.MCPServerSpec{
		PluginID:  pluginID,
		Name:      name,
		Transport: transport,
		Command:   mcp.Command,
		Args:      mcp.Args,
		Env:       mcp.Env,
		WorkDir:   resolvedWorkDir,
		URL:       mcp.URL,
		Headers:   mcp.Headers,
	}, nil
}

func parseClaudePluginRaw(root, manifestPath string) (plugin.InstalledPlugin, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	var manifest ClaudePluginJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		return plugin.InstalledPlugin{}, err
	}

	pluginID := strings.ToLower(filepath.Base(root))
	p := plugin.InstalledPlugin{
		ID:          pluginID,
		Name:        firstNonEmpty(manifest.Name, pluginID),
		Version:     manifest.Version,
		Root:        root,
		Manifest:    manifestPath,
		Kind:        plugin.ManifestKindClaude,
		Description: manifest.Description,
	}

	hooks, err := parseHooksFile(root, pluginID)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	p.Hooks = append(p.Hooks, hooks...)

	return p, nil
}

func skillContributionsFromObjects(root, pluginID string, objects []CaelisSkillContribution) ([]plugin.SkillContribution, error) {
	if len(objects) == 0 {
		return nil, nil
	}
	out := make([]plugin.SkillContribution, 0, len(objects))
	for _, sc := range objects {
		if strings.TrimSpace(sc.Root) == "" {
			continue
		}
		resolvedRoot, err := ResolveSafePath(root, sc.Root)
		if err != nil {
			return nil, err
		}
		namespace := strings.TrimSpace(sc.Namespace)
		if namespace == "" {
			namespace = pluginID
		}
		out = append(out, plugin.SkillContribution{
			Namespace: namespace,
			Root:      resolvedRoot,
			Disabled:  append([]string(nil), sc.Disabled...),
		})
	}
	return out, nil
}

func caelisHooksFromManifest(root, pluginID string, hooks map[string][]CaelisHookSpec) ([]plugin.HookSpec, error) {
	if len(hooks) == 0 {
		return nil, nil
	}
	var out []plugin.HookSpec
	for _, eventName := range sortedKeys(hooks) {
		event := plugin.HookEvent(eventName)
		for _, spec := range hooks[eventName] {
			var resolvedWorkDir string
			if spec.WorkDir != "" {
				var err error
				resolvedWorkDir, err = ResolveSafePath(root, spec.WorkDir)
				if err != nil {
					return nil, fmt.Errorf("pluginregistry: invalid workDir %q: %w", spec.WorkDir, err)
				}
			} else {
				resolvedWorkDir = root
			}
			out = append(out, plugin.HookSpec{
				PluginID:   pluginID,
				Event:      event,
				Command:    spec.Command,
				Args:       append([]string(nil), spec.Args...),
				Env:        cloneStringMap(spec.Env),
				WorkDir:    resolvedWorkDir,
				Timeout:    spec.Timeout,
				RawCommand: spec.Command,
				PluginDir:  root,
			})
		}
	}
	return out, nil
}

func parseHooksFile(root, pluginID string) ([]plugin.HookSpec, error) {
	return parseHooksFileAt(root, pluginID, filepath.Join(root, "hooks", "hooks.json"), false)
}

func parseHooksFileAt(root, pluginID string, hooksPath string, required bool) ([]plugin.HookSpec, error) {
	hooksData, err := os.ReadFile(hooksPath)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hooks.json: %w", err)
	}
	var hookManifest ClaudeHooksJSON
	if err := json.Unmarshal(hooksData, &hookManifest); err != nil {
		return nil, fmt.Errorf("decode hooks.json: %w", err)
	}
	var out []plugin.HookSpec
	for _, eventName := range sortedKeys(hookManifest.Hooks) {
		groups := hookManifest.Hooks[eventName]
		event := plugin.HookEvent(eventName)
		for _, group := range groups {
			for _, hook := range group.Hooks {
				cmd, args, err := SplitCommand(hook.Command)
				if err != nil {
					return nil, fmt.Errorf("parse hook command %q: %w", hook.Command, err)
				}
				if len(hook.Args) > 0 {
					args = append(args, hook.Args...)
				}
				out = append(out, plugin.HookSpec{
					PluginID:   pluginID,
					Event:      event,
					Command:    cmd,
					Args:       args,
					RawCommand: hook.Command,
					WorkDir:    root,
					PluginDir:  root,
				})
			}
		}
	}
	return out, nil
}

func mcpServerSpecsFromMap(root, pluginID string, servers map[string]CaelisMCPServerSpec) ([]plugin.MCPServerSpec, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	out := make([]plugin.MCPServerSpec, 0, len(servers))
	for _, name := range sortedKeys(servers) {
		spec, err := buildMCPServerSpec(root, pluginID, name, servers[name])
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func agentContributionsFromManifest(root string, agents []CaelisAgentContribution) ([]plugin.AgentContribution, error) {
	if len(agents) == 0 {
		return nil, nil
	}
	out := make([]plugin.AgentContribution, 0, len(agents))
	for _, agent := range agents {
		var resolvedWorkDir string
		if agent.WorkDir != "" {
			var err error
			resolvedWorkDir, err = ResolveSafePath(root, agent.WorkDir)
			if err != nil {
				return nil, fmt.Errorf("pluginregistry: invalid workDir %q: %w", agent.WorkDir, err)
			}
		} else {
			resolvedWorkDir = root
		}
		out = append(out, plugin.AgentContribution{
			Name:        agent.Name,
			Description: agent.Description,
			Command:     agent.Command,
			Args:        append([]string(nil), agent.Args...),
			Env:         cloneStringMap(agent.Env),
			WorkDir:     resolvedWorkDir,
		})
	}
	return out, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// SplitCommand parses a command line string into a command and slice of arguments,
// respecting double and single quotes.
func SplitCommand(cmdLine string) (string, []string, error) {
	var args []string
	var current strings.Builder
	inDoubleQuotes := false
	inSingleQuotes := false
	escaped := false

	trimmed := strings.TrimSpace(cmdLine)
	if trimmed == "" {
		return "", nil, nil
	}

	for i := 0; i < len(trimmed); i++ {
		r := trimmed[i]
		if escaped {
			current.WriteByte(r)
			escaped = false
			continue
		}

		if r == '\\' && !inSingleQuotes {
			escaped = true
			continue
		}

		if r == '"' && !inSingleQuotes {
			inDoubleQuotes = !inDoubleQuotes
			continue
		}

		if r == '\'' && !inDoubleQuotes {
			inSingleQuotes = !inSingleQuotes
			continue
		}

		if (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inDoubleQuotes && !inSingleQuotes {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteByte(r)
	}

	if inDoubleQuotes || inSingleQuotes {
		return "", nil, fmt.Errorf("unclosed quote in command: %s", cmdLine)
	}
	if escaped {
		return "", nil, fmt.Errorf("trailing backslash in command: %s", cmdLine)
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	if len(args) == 0 {
		return "", nil, nil
	}

	return args[0], args[1:], nil
}

func addImplicitSkills(root, pluginID string, p *plugin.InstalledPlugin) {
	for _, skillsDir := range []string{
		filepath.Join(root, "skills"),
		filepath.Join(root, ".claude", "skills"),
	} {
		if info, err := os.Stat(skillsDir); err != nil || !info.IsDir() {
			continue
		}
		p.Skills = append(p.Skills, plugin.SkillContribution{
			Namespace: pluginID,
			Root:      skillsDir,
		})
	}
}

func sortedKeys[V any](values map[string]V) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(a, b string) string {
	a = strings.TrimSpace(a)
	if a != "" {
		return a
	}
	return strings.TrimSpace(b)
}
