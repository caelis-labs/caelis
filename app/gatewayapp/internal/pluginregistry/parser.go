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

type GeminiPluginJSON struct {
	Name            string                         `json:"name"`
	Version         string                         `json:"version"`
	Description     string                         `json:"description"`
	ContextFileName string                         `json:"contextFileName"`
	MCPServers      map[string]CaelisMCPServerSpec `json:"mcpServers"`
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

type CodexPluginJSON struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

func ResolveSafePath(pluginRoot, relativePath string) (string, error) {
	cleanRoot := filepath.Clean(pluginRoot)
	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		realRoot = cleanRoot
	} else {
		realRoot = filepath.Clean(realRoot)
	}

	joined := filepath.Join(realRoot, relativePath)
	cleanJoined := filepath.Clean(joined)

	// 1. Lexical prefix boundary check
	if !strings.HasPrefix(cleanJoined, realRoot+string(filepath.Separator)) && cleanJoined != realRoot {
		return "", fmt.Errorf("pluginregistry: path traversal escape detected: %s escapes %s", relativePath, pluginRoot)
	}

	// 2. Physical boundary check by resolving symlinks on the longest existing parent path
	current := cleanJoined
	var suffixParts []string
	var realPrefix string
	for {
		realCurrent, err := filepath.EvalSymlinks(current)
		if err == nil {
			realCurrent = filepath.Clean(realCurrent)
			if !strings.HasPrefix(realCurrent, realRoot+string(filepath.Separator)) && realCurrent != realRoot {
				return "", fmt.Errorf("pluginregistry: path traversal escape detected (via parent symlink): %s escapes %s", relativePath, pluginRoot)
			}
			realPrefix = realCurrent
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
		resolved := realPrefix
		for _, part := range suffixParts {
			resolved = filepath.Join(resolved, part)
		}
		return filepath.Clean(resolved), nil
	}

	return cleanJoined, nil
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

	// 2. Claude Code manifest (takes precedence over Gemini/Codex for hooks)
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

	// 3. Gemini CLI manifest
	geminiPath := filepath.Join(root, "gemini-extension.json")
	if _, err := os.Stat(geminiPath); err == nil {
		hasAnyManifest = true
		gp, err := parseGeminiPluginRaw(root, geminiPath)
		if err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: parse gemini manifest: %w", err)
		}
		if p.Kind == "" {
			p.Kind = plugin.ManifestKindGemini
			p.Manifest = geminiPath
		}
		mergeInstalledPlugin(&p, gp)
	}

	// 4. Codex manifest
	codexPath := filepath.Join(root, ".codex-plugin", "plugin.json")
	if _, err := os.Stat(codexPath); err == nil {
		hasAnyManifest = true
		cxp, err := parseCodexPluginRaw(root, codexPath)
		if err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: parse codex manifest: %w", err)
		}
		if p.Kind == "" {
			p.Kind = plugin.ManifestKindCodex
			p.Manifest = codexPath
		}
		mergeInstalledPlugin(&p, cxp)
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

	for _, sc := range manifest.Skills {
		resolvedRoot, err := ResolveSafePath(root, sc.Root)
		if err != nil {
			return plugin.InstalledPlugin{}, err
		}
		p.Skills = append(p.Skills, plugin.SkillContribution{
			Namespace: sc.Namespace,
			Root:      resolvedRoot,
			Disabled:  sc.Disabled,
		})
	}

	for _, eventName := range sortedKeys(manifest.Hooks) {
		specs := manifest.Hooks[eventName]
		event := plugin.HookEvent(eventName)
		for _, spec := range specs {
			var resolvedWorkDir string
			if spec.WorkDir != "" {
				var err error
				resolvedWorkDir, err = ResolveSafePath(root, spec.WorkDir)
				if err != nil {
					return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: invalid workDir %q: %w", spec.WorkDir, err)
				}
			} else {
				resolvedWorkDir = root
			}
			p.Hooks = append(p.Hooks, plugin.HookSpec{
				PluginID:   pluginID,
				Event:      event,
				Command:    spec.Command,
				Args:       spec.Args,
				Env:        spec.Env,
				WorkDir:    resolvedWorkDir,
				Timeout:    spec.Timeout,
				RawCommand: spec.Command,
				PluginDir:  root,
			})
		}
	}

	for _, name := range sortedKeys(manifest.MCPServers) {
		mcp := manifest.MCPServers[name]
		spec, err := buildMCPServerSpec(root, pluginID, name, mcp)
		if err != nil {
			return plugin.InstalledPlugin{}, err
		}
		p.MCPServers = append(p.MCPServers, spec)
	}

	for _, agent := range manifest.Agents {
		var resolvedWorkDir string
		if agent.WorkDir != "" {
			var err error
			resolvedWorkDir, err = ResolveSafePath(root, agent.WorkDir)
			if err != nil {
				return plugin.InstalledPlugin{}, fmt.Errorf("pluginregistry: invalid workDir %q: %w", agent.WorkDir, err)
			}
		} else {
			resolvedWorkDir = root
		}
		p.Agents = append(p.Agents, plugin.AgentContribution{
			Name:        agent.Name,
			Description: agent.Description,
			Command:     agent.Command,
			Args:        agent.Args,
			Env:         agent.Env,
			WorkDir:     resolvedWorkDir,
		})
	}

	return p, nil
}

func parseGeminiPluginRaw(root, manifestPath string) (plugin.InstalledPlugin, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	var manifest GeminiPluginJSON
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
		Kind:        plugin.ManifestKindGemini,
		Description: manifest.Description,
	}

	for _, name := range sortedKeys(manifest.MCPServers) {
		mcp := manifest.MCPServers[name]
		spec, err := buildMCPServerSpec(root, pluginID, name, mcp)
		if err != nil {
			return plugin.InstalledPlugin{}, err
		}
		p.MCPServers = append(p.MCPServers, spec)
	}

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

	hooksPath := filepath.Join(root, "hooks", "hooks.json")
	if _, err := os.Stat(hooksPath); err == nil {
		hooksData, err := os.ReadFile(hooksPath)
		if err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("read hooks.json: %w", err)
		}
		var hookManifest ClaudeHooksJSON
		if err := json.Unmarshal(hooksData, &hookManifest); err != nil {
			return plugin.InstalledPlugin{}, fmt.Errorf("decode hooks.json: %w", err)
		}
		for _, eventName := range sortedKeys(hookManifest.Hooks) {
			groups := hookManifest.Hooks[eventName]
			event := plugin.HookEvent(eventName)
			for _, group := range groups {
				for _, hook := range group.Hooks {
					cmd, args, err := SplitCommand(hook.Command)
					if err != nil {
						return plugin.InstalledPlugin{}, fmt.Errorf("parse hook command %q: %w", hook.Command, err)
					}
					if len(hook.Args) > 0 {
						args = append(args, hook.Args...)
					}
					p.Hooks = append(p.Hooks, plugin.HookSpec{
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
	} else if !os.IsNotExist(err) {
		return plugin.InstalledPlugin{}, fmt.Errorf("stat hooks.json: %w", err)
	}

	return p, nil
}

func parseCodexPluginRaw(root, manifestPath string) (plugin.InstalledPlugin, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	var manifest CodexPluginJSON
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
		Kind:        plugin.ManifestKindCodex,
		Description: manifest.Description,
	}

	return p, nil
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
	skillsDir := filepath.Join(root, "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
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
