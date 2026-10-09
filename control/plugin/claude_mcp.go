package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parseClaudeMCPServers merges the plugin-root .mcp.json with the supported
// inline manifest map. A manifest entry wins by server name. Both feed the
// existing MCP contribution and Manager path, never a second plugin runtime.
func parseClaudeMCPServers(root, pluginID string, inline json.RawMessage) ([]MCPServerSpec, []string) {
	merged := map[string]MCPServerSpec{}
	var warnings []string
	path, err := ResolveSafePath(root, ".mcp.json")
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Claude .mcp.json ignored: %v", err))
	} else if data, err := os.ReadFile(path); err == nil {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(data, &top); err != nil || top == nil {
			warnings = append(warnings, "Claude .mcp.json ignored: invalid JSON object")
		} else {
			servers := data
			if raw, ok := top["mcpServers"]; ok {
				servers = raw
			}
			if err := mergeClaudeMCPServerMap(merged, servers, root, pluginID, &warnings, "Claude .mcp.json"); err != nil {
				warnings = append(warnings, "Claude .mcp.json ignored: invalid mcpServers map")
			}
		}
	} else if !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("Claude .mcp.json ignored: %v", err))
	}
	if len(inline) > 0 && string(inline) != "null" {
		if err := mergeClaudeMCPServerMap(merged, inline, root, pluginID, &warnings, "Claude manifest mcpServers"); err != nil {
			warnings = append(warnings, "Claude manifest mcpServers ignored: only inline server maps are supported")
		}
	}
	var out []MCPServerSpec
	for _, name := range sortedKeys(merged) {
		out = append(out, merged[name])
	}
	return out, warnings
}

func mergeClaudeMCPServerMap(merged map[string]MCPServerSpec, raw json.RawMessage, root, pluginID string, warnings *[]string, source string) error {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return fmt.Errorf("mcpServers must be an object")
	}
	for _, name := range sortedKeys(entries) {
		var cfg CaelisMCPServerSpec
		if err := json.Unmarshal(entries[name], &cfg); err != nil || string(entries[name]) == "null" {
			*warnings = append(*warnings, fmt.Sprintf("%s server %q skipped: invalid server configuration", source, name))
			continue
		}
		spec, err := buildClaudeMCPServerSpec(root, pluginID, name, cfg)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("%s server %q skipped: %v", source, name, err))
			continue
		}
		merged[name] = spec
	}
	return nil
}

func buildClaudeMCPServerSpec(root, pluginID, name string, cfg CaelisMCPServerSpec) (MCPServerSpec, error) {
	if strings.TrimSpace(name) == "" {
		return MCPServerSpec{}, fmt.Errorf("empty server name")
	}
	transport := NormalizeMCPTransport(firstNonEmpty(cfg.Transport, cfg.Type), cfg.Command, cfg.URL)
	if transport != MCPTransportStdio && transport != MCPTransportStreamableHTTP && transport != MCPTransportSSE {
		return MCPServerSpec{}, fmt.Errorf("unsupported transport %q", transport)
	}
	rootValue := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		rootValue = real
	}
	expand := func(value string) (string, error) {
		value = strings.ReplaceAll(value, "${CLAUDE_PLUGIN_ROOT}", rootValue)
		if strings.Contains(strings.ReplaceAll(value, "${CLAUDE_PROJECT_DIR}", ""), "${") {
			return "", fmt.Errorf("unsupported variable in %q", value)
		}
		return value, nil
	}
	spec := MCPServerSpec{PluginID: pluginID, Name: name, Transport: transport}
	if transport == MCPTransportStdio {
		if strings.TrimSpace(cfg.Command) == "" {
			return MCPServerSpec{}, fmt.Errorf("missing stdio command")
		}
		command, err := expand(cfg.Command)
		if err != nil {
			return MCPServerSpec{}, err
		}
		if strings.Contains(command, "${CLAUDE_PROJECT_DIR}") {
			return MCPServerSpec{}, fmt.Errorf("project-owned executable is not supported")
		}
		const rootVariable = "${CLAUDE_PLUGIN_ROOT}"
		switch {
		case strings.HasPrefix(cfg.Command, rootVariable):
			relative := strings.TrimPrefix(cfg.Command, rootVariable)
			if relative == "" || (relative[0] != '/' && relative[0] != '\\') {
				return MCPServerSpec{}, fmt.Errorf("invalid CLAUDE_PLUGIN_ROOT command path")
			}
			relative = strings.ReplaceAll(relative[1:], `\`, string(filepath.Separator))
			if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
				return MCPServerSpec{}, fmt.Errorf("invalid relative command path")
			}
			command, err = ResolveSafePath(rootValue, relative)
		case strings.Contains(cfg.Command, rootVariable):
			return MCPServerSpec{}, fmt.Errorf("CLAUDE_PLUGIN_ROOT command must start at plugin root")
		case !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`):
			relative := strings.ReplaceAll(command, `\`, string(filepath.Separator))
			if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
				return MCPServerSpec{}, fmt.Errorf("invalid relative command path")
			}
			command, err = ResolveSafePath(rootValue, relative)
		}
		if err != nil {
			return MCPServerSpec{}, err
		}
		spec.Command = command
		cwd := firstNonEmpty(cfg.WorkDir, cfg.CWD)
		if cwd == "" {
			spec.WorkDir = rootValue
		} else {
			cwd, err = expand(cwd)
			if err != nil {
				return MCPServerSpec{}, err
			}
			if strings.HasPrefix(cwd, "${CLAUDE_PROJECT_DIR}") {
				spec.WorkDir = cwd // bounded to the Session workspace at assembly
			} else {
				if filepath.IsAbs(cwd) {
					if !PathWithinRoot(rootValue, cwd) {
						return MCPServerSpec{}, fmt.Errorf("cwd escapes plugin root")
					}
					cwd, err = filepath.Rel(rootValue, cwd)
					if err != nil {
						return MCPServerSpec{}, err
					}
				}
				spec.WorkDir, err = ResolveSafePath(rootValue, cwd)
				if err != nil {
					return MCPServerSpec{}, err
				}
			}
		}
		for _, value := range cfg.Args {
			expanded, err := expand(value)
			if err != nil {
				return MCPServerSpec{}, err
			}
			spec.Args = append(spec.Args, expanded)
		}
		spec.Env = make(map[string]string, len(cfg.Env)+1)
		for key, value := range cfg.Env {
			if strings.EqualFold(key, "CLAUDE_PLUGIN_ROOT") {
				continue // the canonical package root belongs to the Host
			}
			expanded, err := expand(value)
			if err != nil {
				return MCPServerSpec{}, err
			}
			spec.Env[key] = expanded
		}
		spec.Env["CLAUDE_PLUGIN_ROOT"] = rootValue
		spec.CleanEnvironment = true
	} else {
		var err error
		spec.URL, err = expand(cfg.URL)
		if err != nil {
			return MCPServerSpec{}, err
		}
		spec.Headers = make(map[string]string, len(cfg.Headers))
		for key, value := range cfg.Headers {
			expanded, err := expand(value)
			if err != nil {
				return MCPServerSpec{}, err
			}
			spec.Headers[key] = expanded
		}
	}
	return spec, nil
}

func instantiateClaudeProjectDir(spec MCPServerSpec, projectDir string) (MCPServerSpec, error) {
	const placeholder = "${CLAUDE_PROJECT_DIR}"
	hasProjectDir := strings.Contains(spec.Command, placeholder) || strings.Contains(spec.WorkDir, placeholder) || strings.Contains(spec.URL, placeholder)
	for _, value := range spec.Args {
		hasProjectDir = hasProjectDir || strings.Contains(value, placeholder)
	}
	for _, value := range spec.Env {
		hasProjectDir = hasProjectDir || strings.Contains(value, placeholder)
	}
	for _, value := range spec.Headers {
		hasProjectDir = hasProjectDir || strings.Contains(value, placeholder)
	}
	if !hasProjectDir {
		return spec, nil
	}
	if !filepath.IsAbs(projectDir) {
		return MCPServerSpec{}, fmt.Errorf("CLAUDE_PROJECT_DIR requires an absolute Session workspace")
	}
	projectDir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return MCPServerSpec{}, err
	}
	replace := func(value string) string { return strings.ReplaceAll(value, placeholder, projectDir) }
	if strings.HasPrefix(spec.WorkDir, placeholder) {
		relative := strings.TrimPrefix(spec.WorkDir, placeholder)
		if relative != "" && !strings.HasPrefix(relative, "/") {
			return MCPServerSpec{}, fmt.Errorf("invalid CLAUDE_PROJECT_DIR cwd")
		}
		resolved, err := ResolveSafePath(projectDir, strings.TrimPrefix(relative, "/"))
		if err != nil {
			return MCPServerSpec{}, err
		}
		spec.WorkDir = resolved
	} else if strings.Contains(spec.WorkDir, placeholder) {
		return MCPServerSpec{}, fmt.Errorf("CLAUDE_PROJECT_DIR cwd must be rooted at the Session workspace")
	}
	for i := range spec.Args {
		spec.Args[i] = replace(spec.Args[i])
	}
	for key, value := range spec.Env {
		spec.Env[key] = replace(value)
	}
	for key, value := range spec.Headers {
		spec.Headers[key] = replace(value)
	}
	spec.URL = replace(spec.URL)
	return spec, nil
}
