package acpinstall

import (
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/control/agents"
)

// manualSetupSteps describes the same complete bundle used by Install. These
// are Caelis instructions derived from the registry, not an upstream tutorial.
func manualSetupSteps(setup agents.RuntimeSetup, goos string) []string {
	steps := []string{
		"1. Download the ZIP using the link above.",
		"2. Create this directory. Extract all files together into it:\n" + setup.Directory,
	}
	if goos != "windows" {
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		steps = append(steps, "3. In a terminal on the Host, run:\ncd "+quote(setup.Directory)+"\nchmod +x "+quote(setup.Command)+"\n[ ! -f localharness_external ] || chmod +x localharness_external")
	}
	steps = append(steps, strconv.Itoa(len(steps)+1)+". Choose Check installation to sign in and select a model.")
	steps = append(steps, "For updates, stop Antigravity sessions before replacing the entire runtime bundle.")
	return steps
}
