package setupcmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setup"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
)

const internalSetupHelperCommand = "__caelis_windows_sandbox_setup__"

func Run(args []string, stderr io.Writer) int {
	args = normalizeArgs(args)
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: caelis-windows-sandbox-setup [__caelis_windows_sandbox_setup__] <base64-payload>")
		return 2
	}
	payload, err := setup.DecodePayload(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := setup.Execute(payload); err != nil {
		if payload.Kind == setup.SetupKindReadACLRefresh {
			fmt.Fprintln(stderr, err)
			return 1
		}
		dirs := setupstate.NewDirs(payload.StateRoot)
		errorPath := dirs.ErrorPath
		if payload.Kind == setup.SetupKindReset {
			errorPath = dirs.ResetErrorPath
		}
		if _, readErr := setupstate.ReadError(errorPath); readErr != nil {
			_ = setupstate.WriteError(errorPath, setupstate.ErrorReport{
				Phase:   "setup",
				Code:    "setup_failed",
				Message: err.Error(),
			})
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), internalSetupHelperCommand) {
		return args[1:]
	}
	return args
}
