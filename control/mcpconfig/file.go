package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type overlayDocument struct {
	MCPServers map[string]overlayServerConfig `json:"mcpServers"`
}

type overlayServerConfig struct {
	ServerConfig
	WorkDirCamel string `json:"workDir"`
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
