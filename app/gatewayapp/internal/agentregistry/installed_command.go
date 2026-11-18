package agentregistry

import "os/exec"

// FindInstalledCommand returns the first preset command available on PATH.
func FindInstalledCommand(name string) string {
	for _, command := range InstalledAgentCommandCandidates(name) {
		if _, err := exec.LookPath(command); err == nil {
			return command
		}
	}
	return ""
}
