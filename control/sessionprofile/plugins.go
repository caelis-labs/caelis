package sessionprofile

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func validatePlugins(plugins []Plugin) error {
	if len(plugins) > 16 {
		return fmt.Errorf("%w: too many native plugins", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, selected := range plugins {
		if !filepath.IsAbs(selected.Root) || filepath.Clean(selected.Root) != selected.Root || strings.TrimSpace(selected.Root) != selected.Root || len(selected.Root) > 4096 || seen[selected.Root] || len(selected.MCPServers) > 16 {
			return fmt.Errorf("%w: duplicate or invalid native plugin root", ErrInvalid)
		}
		seen[selected.Root] = true
		servers := map[string]bool{}
		for _, proxy := range selected.MCPServers {
			if proxy.Name == "" || utf8.RuneCountInString(proxy.Name) > 128 || strings.TrimSpace(proxy.Name) != proxy.Name || servers[proxy.Name] || proxy.Transport != "stdio" || strings.TrimSpace(proxy.Command) == "" || len(proxy.Command) > 4096 || proxy.WorkDir != selected.Root || proxy.URL != "" || len(proxy.Args) > 64 {
				return fmt.Errorf("%w: native plugin proxy requires a unique declared server, stdio command and package work_dir", ErrInvalid)
			}
			servers[proxy.Name] = true
			for _, arg := range proxy.Args {
				if len(arg) > 4096 {
					return fmt.Errorf("%w: native plugin proxy argument is too long", ErrInvalid)
				}
			}
		}
	}
	return nil
}

// UnmarshalJSON rejects explicit nulls and undeclared package authority fields.
func (p *Plugin) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalid
	}
	for name, value := range fields {
		if (name != "root" && name != "mcp_servers") || string(value) == "null" {
			return ErrInvalid
		}
	}
	type alias Plugin
	var result alias
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*p = Plugin(result)
	return nil
}
