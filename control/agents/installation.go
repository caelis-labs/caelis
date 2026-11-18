package agents

import "strings"

// RuntimeInstallation retains the archive and destination in legacy ACP
// onboarding payloads and preparation records. The Host rejects new install
// requests.
type RuntimeInstallation struct {
	Directory  string `json:"directory"`
	ArchiveURL string `json:"archive_url"`
	SHA256     string `json:"sha256,omitempty"`
}

// RuntimeSetup retains installation details in the legacy completion contract.
type RuntimeSetup struct {
	Command    string `json:"command"`
	Directory  string `json:"directory"`
	ArchiveURL string `json:"archive_url,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// ManualSteps are Host-specific instructions supplied by Caelis.
	ManualSteps []string `json:"manual_steps,omitempty"`
	// InstallPrompt describes an installation task for the user's current Agent.
	// It contains the Host platform and resolved official bundle, not client paths.
	InstallPrompt string `json:"install_prompt,omitempty"`
}

// NormalizeRuntimeInstallation returns a detached installation confirmation.
func NormalizeRuntimeInstallation(in *RuntimeInstallation) *RuntimeInstallation {
	if in == nil {
		return nil
	}
	return &RuntimeInstallation{Directory: strings.TrimSpace(in.Directory),
		ArchiveURL: strings.TrimSpace(in.ArchiveURL), SHA256: strings.ToLower(strings.TrimSpace(in.SHA256))}
}
