package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"golang.org/x/net/http/httpguts"
)

const (
	agentPluginManifestSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	agentPluginMCPSchema      = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

var agentPluginName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var executableEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type agentPluginManifest struct {
	Schema      string `json:"$schema"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		URL   string `json:"url"`
	} `json:"author"`
	Homepage   string          `json:"homepage"`
	Repository string          `json:"repository"`
	License    string          `json:"license"`
	Keywords   []string        `json:"keywords"`
	Extensions json.RawMessage `json:"extensions"`
}

type agentPluginServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	CWD     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// parseAgentPlugin is the single authoritative Agent Plugins 1.0 path. Its
// fixed components normalize into the same Skill and MCP contributions used by
// native and Claude plugins; it never creates another runtime or tool registry.
func parseAgentPlugin(root string) (InstalledPlugin, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return InstalledPlugin{}, fmt.Errorf("agent plugin root: %w", err)
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return InstalledPlugin{}, err
	}
	manifestPath, err := ResolveSafePath(realRoot, "plugin.json")
	if err != nil {
		return InstalledPlugin{}, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return InstalledPlugin{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return InstalledPlugin{}, fmt.Errorf("agent plugin manifest is not a JSON object: %w", err)
	}
	if fields == nil {
		return InstalledPlugin{}, fmt.Errorf("agent plugin manifest is not a JSON object")
	}
	var manifest agentPluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return InstalledPlugin{}, fmt.Errorf("agent plugin manifest: %w", err)
	}
	for field, kind := range map[string]byte{
		"$schema": '"', "name": '"', "version": '"', "description": '"',
		"author": '{', "homepage": '"', "repository": '"', "license": '"', "keywords": '[',
	} {
		if raw, ok := fields[field]; ok && !jsonFieldKind(raw, kind) {
			return InstalledPlugin{}, fmt.Errorf("agent plugin: invalid %s field", field)
		}
	}
	if raw, ok := fields["author"]; ok {
		var author map[string]json.RawMessage
		_ = json.Unmarshal(raw, &author)
		for name, value := range author {
			if !slices.Contains([]string{"name", "email", "url"}, name) || !jsonFieldKind(value, '"') {
				return InstalledPlugin{}, fmt.Errorf("agent plugin: invalid author.%s field", name)
			}
		}
	}
	if manifest.Schema != agentPluginManifestSchema {
		return InstalledPlugin{}, fmt.Errorf("agent plugin: unsupported manifest schema %q", manifest.Schema)
	}
	if len(manifest.Name) > 64 || !agentPluginName.MatchString(manifest.Name) || strings.Contains(manifest.Name, "--") || strings.Contains(manifest.Name, "..") {
		return InstalledPlugin{}, fmt.Errorf("agent plugin: invalid name %q", manifest.Name)
	}
	p := InstalledPlugin{
		ID:          strings.ToLower(filepath.Base(root)),
		Name:        manifest.Name,
		Version:     manifest.Version,
		Root:        realRoot,
		Manifest:    manifestPath,
		Kind:        ManifestKindAgentPlugin,
		Description: manifest.Description,
	}
	known := []string{"$schema", "name", "version", "description", "author", "homepage", "repository", "license", "keywords", "extensions"}
	for field := range fields {
		if !slices.Contains(known, field) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("unknown Agent Plugins manifest field %q ignored", field))
		}
	}
	if len(manifest.Extensions) > 0 && manifest.Extensions[0] != '{' {
		p.Warnings = append(p.Warnings, "non-object Agent Plugins extensions ignored")
	}
	skillPath, err := ResolveSafePath(realRoot, "skills")
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Agent Plugins skills ignored: %v", err))
	} else if info, statErr := os.Stat(skillPath); statErr == nil {
		if info.IsDir() {
			p.Skills = []SkillContribution{{Namespace: p.ID, Root: skillPath}}
		} else {
			p.Warnings = append(p.Warnings, "Agent Plugins skills path is not a directory")
		}
	} else if !os.IsNotExist(statErr) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Agent Plugins skills ignored: %v", statErr))
	}
	specs, warnings := parseAgentPluginMCP(realRoot, p.ID)
	p.MCPServers = specs
	p.Warnings = append(p.Warnings, warnings...)
	slices.Sort(p.Warnings)
	return p, nil
}

func parseAgentPluginMCP(root, pluginID string) ([]MCPServerSpec, []string) {
	path, err := ResolveSafePath(root, "mcp.json")
	if err != nil {
		return nil, []string{fmt.Sprintf("Agent Plugins MCP disabled: %v", err)}
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, []string{"Agent Plugins MCP disabled: mcp.json is not a regular file"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("Agent Plugins MCP disabled: %v", err)}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return nil, []string{"Agent Plugins MCP disabled: invalid JSON object"}
	}
	if len(top) != 2 || top["$schema"] == nil || top["mcpServers"] == nil {
		return nil, []string{"Agent Plugins MCP disabled: mcp.json requires only $schema and mcpServers"}
	}
	var schema string
	if err := json.Unmarshal(top["$schema"], &schema); err != nil || schema != agentPluginMCPSchema {
		return nil, []string{"Agent Plugins MCP disabled: unsupported or mismatched mcp.json schema"}
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(top["mcpServers"], &servers); err != nil || servers == nil {
		return nil, []string{"Agent Plugins MCP disabled: mcpServers must be an object"}
	}
	var out []MCPServerSpec
	var warnings []string
	for _, name := range sortedKeys(servers) {
		spec, err := parseAgentPluginServer(root, pluginID, name, servers[name])
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Agent Plugins MCP server %q skipped: %v", name, err))
			continue
		}
		out = append(out, spec)
	}
	return out, warnings
}

func parseAgentPluginServer(root, pluginID, name string, raw json.RawMessage) (MCPServerSpec, error) {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 128 {
		return MCPServerSpec{}, fmt.Errorf("invalid server name")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return MCPServerSpec{}, fmt.Errorf("server must be a JSON object")
	}
	var cfg agentPluginServer
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return MCPServerSpec{}, err
	}
	for field, kind := range map[string]byte{
		"type": '"', "command": '"', "args": '[', "env": '{',
		"cwd": '"', "url": '"', "headers": '{',
	} {
		if value, ok := fields[field]; ok && !jsonFieldKind(value, kind) {
			return MCPServerSpec{}, fmt.Errorf("invalid %s field", field)
		}
	}
	spec := MCPServerSpec{PluginID: pluginID, Name: name}
	var allowed []string
	switch cfg.Type {
	case "stdio":
		allowed = []string{"type", "command", "args", "env", "cwd"}
		if cfg.Command == "" || strings.Contains(cfg.Command, "${") || filepath.IsAbs(cfg.Command) {
			return MCPServerSpec{}, fmt.Errorf("stdio command must be one bare name or ./ plugin path")
		}
		if strings.HasPrefix(cfg.Command, "./") {
			command, err := ResolveSafePath(root, cfg.Command)
			if err != nil {
				return MCPServerSpec{}, err
			}
			if info, err := os.Stat(command); err != nil || !info.Mode().IsRegular() {
				return MCPServerSpec{}, fmt.Errorf("plugin command is not a regular file")
			}
			spec.Command = command
		} else if strings.ContainsAny(cfg.Command, `/\\`) || cfg.Command == "." || cfg.Command == ".." {
			return MCPServerSpec{}, fmt.Errorf("stdio command must be one bare name or ./ plugin path")
		} else {
			spec.Command = cfg.Command
		}
		if cfg.CWD != "" && cfg.CWD != "${PLUGIN_ROOT}" && cfg.CWD != "${PLUGIN_DATA}" &&
			!strings.HasPrefix(cfg.CWD, "./") && !strings.HasPrefix(cfg.CWD, "${PLUGIN_ROOT}/") && !strings.HasPrefix(cfg.CWD, "${PLUGIN_DATA}/") {
			return MCPServerSpec{}, fmt.Errorf("cwd must be plugin-relative, PLUGIN_ROOT or PLUGIN_DATA")
		}
		if cfg.CWD != "" {
			var base, rel string
			switch {
			case strings.HasPrefix(cfg.CWD, "./"):
				base, rel = root, cfg.CWD
			case strings.HasPrefix(cfg.CWD, "${PLUGIN_ROOT}/"):
				base, rel = root, strings.TrimPrefix(cfg.CWD, "${PLUGIN_ROOT}/")
			case strings.HasPrefix(cfg.CWD, "${PLUGIN_DATA}/"):
				rel = strings.TrimPrefix(cfg.CWD, "${PLUGIN_DATA}/")
				if filepath.IsAbs(rel) || !PathWithinRoot("/plugin-data", filepath.Join("/plugin-data", rel)) {
					return MCPServerSpec{}, fmt.Errorf("cwd escapes PLUGIN_DATA")
				}
			}
			if base != "" {
				if _, err := ResolveSafePath(base, rel); err != nil {
					return MCPServerSpec{}, fmt.Errorf("cwd escapes its root: %w", err)
				}
			}
		}
		for key := range cfg.Env {
			if strings.EqualFold(key, "PLUGIN_ROOT") || strings.EqualFold(key, "PLUGIN_DATA") {
				return MCPServerSpec{}, fmt.Errorf("reserved environment variable %q", key)
			}
		}
		spec.Transport = MCPTransportStdio
		spec.Args = cfg.Args
		spec.Env = cfg.Env
		spec.WorkDir = cfg.CWD
		spec.CleanEnvironment = true
	case "streamable-http", "sse":
		allowed = []string{"type", "url", "headers"}
		endpoint, err := url.Parse(cfg.URL)
		if err != nil || endpoint == nil || !endpoint.IsAbs() || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
			return MCPServerSpec{}, fmt.Errorf("invalid MCP endpoint URL")
		}
		if endpoint.Scheme != "https" {
			ip := net.ParseIP(endpoint.Hostname())
			if endpoint.Scheme != "http" || (endpoint.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
				return MCPServerSpec{}, fmt.Errorf("non-loopback MCP endpoint requires HTTPS")
			}
		}
		if err := validateAgentPluginHeaders(cfg.Headers); err != nil {
			return MCPServerSpec{}, err
		}
		spec.Transport = NormalizeMCPTransport(cfg.Type, "", cfg.URL)
		spec.URL = cfg.URL
		spec.Headers = cfg.Headers
	default:
		return MCPServerSpec{}, fmt.Errorf("unsupported transport %q", cfg.Type)
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return MCPServerSpec{}, fmt.Errorf("field %q is not valid for %s", field, cfg.Type)
		}
	}
	return spec, nil
}

func jsonFieldKind(raw json.RawMessage, kind byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == kind
}

func validateAgentPluginHeaders(headers map[string]string) error {
	seen := map[string]bool{}
	for key, value := range headers {
		if !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("invalid HTTP header")
		}
		folded := strings.ToLower(key)
		if seen[folded] {
			return fmt.Errorf("duplicate HTTP header %q", key)
		}
		seen[folded] = true
	}
	return nil
}

// agentPluginDataDir gives a configured plugin one persistent client-owned
// state directory across package updates without trusting its ID as a path.
func agentPluginDataDir(storeDir, pluginID string) (string, error) {
	if strings.TrimSpace(storeDir) == "" || strings.TrimSpace(pluginID) == "" {
		return "", fmt.Errorf("agent plugins MCP needs a host store and plugin identity")
	}
	root, err := filepath.Abs(storeDir)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(pluginID)))
	return filepath.Join(root, "plugins", "data", hex.EncodeToString(digest[:16])), nil
}

func instantiateAgentPluginMCP(spec MCPServerSpec, root, dataDir string) (MCPServerSpec, error) {
	if spec.Transport != mcp.TransportStdio {
		return spec, nil
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = root
	} else {
		var base, relative string
		switch {
		case strings.HasPrefix(workDir, "./"):
			base, relative = root, workDir
		case workDir == "${PLUGIN_ROOT}":
			base, relative = root, "."
		case strings.HasPrefix(workDir, "${PLUGIN_ROOT}/"):
			base, relative = root, strings.TrimPrefix(workDir, "${PLUGIN_ROOT}/")
		case workDir == "${PLUGIN_DATA}":
			base, relative = dataDir, "."
		case strings.HasPrefix(workDir, "${PLUGIN_DATA}/"):
			base, relative = dataDir, strings.TrimPrefix(workDir, "${PLUGIN_DATA}/")
		default:
			return MCPServerSpec{}, fmt.Errorf("invalid Agent Plugins cwd %q", workDir)
		}
		if base == dataDir {
			// The persistent data directory is created only when the server
			// starts. ResolveSafePath requires an existing root, so keep this
			// path lexical here and check symlinks again at launch.
			resolved := filepath.Clean(filepath.Join(base, relative))
			if !PathWithinRoot(base, resolved) {
				return MCPServerSpec{}, fmt.Errorf("cwd escapes PLUGIN_DATA")
			}
			workDir = resolved
		} else {
			resolved, err := ResolveSafePath(base, relative)
			if err != nil {
				return MCPServerSpec{}, err
			}
			workDir = resolved
		}
	}
	expand := strings.NewReplacer("${PLUGIN_ROOT}", root, "${PLUGIN_DATA}", dataDir).Replace
	spec.WorkDir = workDir
	spec.Args = append([]string(nil), spec.Args...)
	for i := range spec.Args {
		spec.Args[i] = expand(spec.Args[i])
	}
	env := make(map[string]string, len(spec.Env)+2)
	for key, value := range spec.Env {
		env[key] = expand(value)
	}
	env["PLUGIN_ROOT"] = root
	env["PLUGIN_DATA"] = dataDir
	spec.Env = env
	spec.DataDir = dataDir
	return spec, nil
}

func applyHostExecutableEnv(spec MCPServerSpec, paths map[string]string, targetOS string) (MCPServerSpec, error) {
	if spec.Transport != MCPTransportStdio {
		return spec, nil
	}
	// Windows environment names are case-insensitive. Choose a stable package
	// value for case variants, then let explicit Host paths take precedence.
	if targetOS == "windows" {
		normalized := make(map[string]string, len(spec.Env))
		for _, name := range sortedKeys(spec.Env) {
			if !hasFoldedEnvironmentKey(normalized, name) {
				normalized[name] = spec.Env[name]
			}
		}
		spec.Env = normalized
	}
	seenHost := map[string]bool{}
	for _, name := range sortedKeys(paths) {
		configuredPath := paths[name]
		if !executableEnvName.MatchString(name) || strings.EqualFold(name, "PLUGIN_ROOT") || strings.EqualFold(name, "PLUGIN_DATA") {
			return MCPServerSpec{}, fmt.Errorf("invalid host executable environment name %q", name)
		}
		if targetOS == "windows" && seenHost[strings.ToUpper(name)] {
			return MCPServerSpec{}, fmt.Errorf("host executable environment %q conflicts with another host name", name)
		}
		seenHost[strings.ToUpper(name)] = true
		if _, exists := spec.Env[name]; exists && targetOS != "windows" {
			return MCPServerSpec{}, fmt.Errorf("host executable environment %q conflicts with package configuration", name)
		}
		if !filepath.IsAbs(configuredPath) {
			return MCPServerSpec{}, fmt.Errorf("host executable environment %q needs an absolute path", name)
		}
		resolved, err := filepath.EvalSymlinks(configuredPath)
		if err != nil {
			return MCPServerSpec{}, fmt.Errorf("host executable environment %q: %w", name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return MCPServerSpec{}, fmt.Errorf("host executable environment %q is not a file", name)
		}
		if targetOS == "windows" {
			for packageName := range spec.Env {
				if strings.EqualFold(packageName, name) {
					delete(spec.Env, packageName)
				}
			}
		}
		spec.Env[name] = resolved
	}
	return spec, nil
}

func hasFoldedEnvironmentKey(env map[string]string, key string) bool {
	for existing := range env {
		if strings.EqualFold(existing, key) {
			return true
		}
	}
	return false
}
