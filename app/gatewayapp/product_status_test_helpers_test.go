package gatewayapp_test

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func builtinComputerUseSupported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" || runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
}

// Optional capability regressions keep checking their exact source set. Fixed
// product contributions have their own identity and approval assertions.
func optionalCapabilityStatus(status sessionprofile.MCPStatus) sessionprofile.MCPStatus {
	out := status
	out.Servers = nil
	out.Skills = nil
	out.Plugins = nil
	for _, server := range status.Servers {
		if server.PluginID != "desktop-world" {
			out.Servers = append(out.Servers, server)
		}
	}
	for _, skill := range status.Skills {
		if skill.Name != "desktop-world:desktop-world" {
			out.Skills = append(out.Skills, skill)
		}
	}
	for _, p := range status.Plugins {
		if p.ID != "desktop-world" {
			out.Plugins = append(out.Plugins, p)
		}
	}
	return out
}

func builtinComputerUseVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../control/plugin/builtin/desktop-world.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pin); err != nil {
		t.Fatal(err)
	}
	return pin.Version
}
