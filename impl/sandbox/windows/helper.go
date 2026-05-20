//go:build windows

package windows

import (
	"os"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/runnercmd"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/setupcmd"
)

const (
	setupHelperCommand  = "__caelis_windows_sandbox_setup__"
	runnerHelperCommand = "__caelis_windows_command_runner__"
)

func MaybeRunInternalHelper(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case setupHelperCommand:
		os.Exit(setupcmd.Run(args[1:], os.Stderr))
	case runnerHelperCommand:
		os.Exit(runnercmd.Run(os.Stdin, os.Stdout, os.Stderr))
	default:
		return false
	}
	return true
}
