package agentregistry

import (
	"os/exec"
	"path/filepath"
)

// FindInstalledCommand returns the logical PATH command or a runtime at the
// default user installation destination. It never downloads or updates files.
func FindInstalledCommand(name string) string {
	for _, command := range InstalledAgentCommandCandidates(name) {
		if _, err := exec.LookPath(command); err == nil {
			return command
		}
	}
	agent, ok := LookupConnectableAgent(name)
	if !ok || agent.Installation == nil {
		return ""
	}
	setup, err := agent.Installation.Setup()
	if err != nil {
		return ""
	}
	command := filepath.Join(setup.Directory, setup.Command)
	if _, err := exec.LookPath(command); err == nil {
		return command
	}
	return ""
}
