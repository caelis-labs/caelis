package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type overlayDocument struct {
	MCPServers map[string]overlayServerConfig `json:"mcpServers"`
}

type overlayServerConfig struct {
	ServerConfig
	WorkDirCamel string `json:"workDir"`
}

// ProjectFiles returns the supported project MCP overlay paths for a
// workspace. An empty project root has no project overlay paths.
func ProjectFiles(projectRoot string) (agentsFile string, mcpFile string) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return "", ""
	}
	return filepath.Join(projectRoot, ".agents", "mcp.json"), filepath.Join(projectRoot, ".mcp.json")
}

// ProjectOverlayPresent reports whether the workspace contains either
// supported project MCP configuration file. It intentionally checks only file
// presence: project-controlled content is not parsed until workspace trust
// permits Runtime assembly to load the overlay.
func ProjectOverlayPresent(projectRoot string) (bool, error) {
	agentsFile, mcpFile := ProjectFiles(projectRoot)
	if agentsFile == "" || mcpFile == "" {
		return false, nil
	}
	for _, path := range []string{agentsFile, mcpFile} {
		if info, err := os.Stat(path); err == nil {
			if info.Mode().IsRegular() {
				return true, nil
			}
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("mcpconfig: inspect %s: %w", path, err)
		}
	}
	return false, nil
}

func (s overlayServerConfig) serverConfig() ServerConfig {
	out := normalizeServerConfig(s.ServerConfig)
	if out.WorkDir == "" {
		out.WorkDir = strings.TrimSpace(s.WorkDirCamel)
	}
	return out
}

func loadOverlayFile(path string) (Servers, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcpconfig: read %s: %w", path, err)
	}
	servers, err := parseOverlayDocument(data)
	if err != nil {
		return nil, fmt.Errorf("mcpconfig: parse %s: %w", path, err)
	}
	return servers, nil
}

// loadAvailableOverlayFile retains valid siblings in an optional overlay.
// Invalid JSON invalidates only this file; an invalid entry cannot authorize
// a server or prevent lower-priority sources from loading.
func loadAvailableOverlayFile(path string) Servers {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	// Any two raw names that normalize to the same identity are ambiguous. Drop
	// the whole group before parsing content so map iteration order cannot pick
	// which command runs. A healthy lower-priority layer may still provide it.
	counts := make(map[string]int, len(doc.MCPServers))
	for name := range doc.MCPServers {
		if normalized := normalizeServerName(name); normalized != "" {
			counts[normalized]++
		}
	}
	out := make(Servers)
	for name, raw := range doc.MCPServers {
		normalized := normalizeServerName(name)
		if normalized == "" || counts[normalized] != 1 || strings.ContainsAny(name, "/\\") || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var cfg overlayServerConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		out[normalized] = cfg.serverConfig()
	}
	return out
}

func parseOverlayDocument(data []byte) (Servers, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	var doc overlayDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.MCPServers) == 0 {
		return nil, nil
	}
	out := make(Servers, len(doc.MCPServers))
	for name, cfg := range doc.MCPServers {
		out[name] = cfg.serverConfig()
	}
	if err := ValidateIdentities(out); err != nil {
		return nil, err
	}
	return Normalize(out), nil
}
