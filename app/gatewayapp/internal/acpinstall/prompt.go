package acpinstall

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/control/agents"
)

func installPrompt(setup agents.RuntimeSetup, goos, goarch string, args []string) string {
	if args == nil {
		args = []string{}
	}
	facts, _ := json.MarshalIndent(struct {
		OS        string   `json:"os"`
		Arch      string   `json:"arch"`
		Directory string   `json:"directory"`
		Archive   string   `json:"archive_url"`
		SHA256    string   `json:"sha256,omitempty"`
		Command   string   `json:"command"`
		Args      []string `json:"args"`
	}{goos, goarch, setup.Directory, setup.ArchiveURL, setup.SHA256, setup.Command, args}, "", "  ")
	steps := []string{
		"Install the official Google Antigravity ACP runtime for me. Carry out the installation with your tools, then report the result.",
		"The following facts were resolved by Caelis from the official ACP Registry for its Host. Confirm your execution environment matches this OS and architecture and can access this user's destination. If the Host is elsewhere and inaccessible, explain the blocker instead of installing on a different machine.",
		string(facts),
		"Inspect the destination first. Reuse a working installation if present. Otherwise download the specified ZIP to a temporary location, verify SHA-256 when supplied, and extract the complete bundle with all companion files into the destination. Preserve existing user files; do not delete an occupied directory or overwrite unrelated content.",
	}
	if goos == "windows" {
		steps = append(steps, "Use Windows-native tools such as PowerShell Invoke-WebRequest and Expand-Archive with literal, correctly quoted paths. The runtime is an .exe; do not use chmod, POSIX shell syntax, or assume WSL. If Windows blocks the downloaded bundle, report the specific blocker.")
	} else {
		steps = append(steps, "Use tools available on this Host to download and unzip the bundle. Set executable permission on the runtime and localharness_external if present. Quote paths safely, including spaces and single quotes.")
	}
	steps = append(steps,
		"Verify that the runtime and its companions are installed and resolve the executable to its absolute path inside the destination. Perform an ACP initialize handshake over stdio using that path and the arguments above, with the bundle directory as the working directory. Stop the verification process afterward. Report installation and handshake results separately; a file existing alone is not proof that the runtime works.",
		"Keep the bundle user-managed: no .caelis adapter cache, version lock, background updater, or global PATH modification. Do not install the separate agy CLI or a third-party adapter as a substitute.",
		"When complete, give me the installed path and verification result. Tell me to return to /connect, choose Antigravity, and use Check installation if offered to continue sign-in and model selection. Browser sign-in remains my action; if it is required, explain that remaining step without claiming authentication is complete.")
	return strings.Join(steps, "\n\n")
}
