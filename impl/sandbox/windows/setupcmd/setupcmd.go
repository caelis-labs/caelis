package setupcmd

import (
	"fmt"
	"io"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setup"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
)

func Run(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: caelis-windows-sandbox-setup <base64-payload>")
		return 2
	}
	payload, err := setup.DecodePayload(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := setup.Execute(payload); err != nil {
		dirs := setupstate.NewDirs(payload.StateRoot)
		_ = setupstate.WriteError(dirs.ErrorPath, setupstate.ErrorReport{
			Phase:   "setup",
			Code:    "setup_failed",
			Message: err.Error(),
		})
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
