package agentregistry

import "github.com/caelis-labs/caelis/app/gatewayapp/internal/acpinstall"

// Antigravity publishes a standalone runtime in the ACP Registry. The catalog
// declares its upstream source and launch command; explicit setup resolves the
// current release without retaining a managed version.
func antigravityACPAgent(goos string) installedConnectableAgent {
	command := "agy_acp_server.par"
	var args []string
	switch goos {
	case "windows":
		command = "agy_acp_server.exe"
	case "linux":
		args = []string{"--uid="}
	}
	agent := nativeACPAgent("antigravity", "Google Antigravity",
		"Google's coding agent through its official standalone ACP runtime", 6, command, args...)
	agent.Agent.Installation = &acpinstall.Source{
		RegistryURL:   "https://raw.githubusercontent.com/agentclientprotocol/registry/main/antigravity-acp/agent.json",
		DirectoryName: "antigravity-acp", Command: command, Args: args,
		ArchiveHost: "dl.google.com", ArchivePathPrefix: "/agy-extensions/releases/",
	}
	return agent
}
