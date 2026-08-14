//go:build windows

package windows

import (
	"fmt"
	"os"
)

const internalRepairHelperCommand = "__caelis_windows_sandbox_fix__"

func MaybeRunInternalHelper(args []string) bool {
	if len(args) == 0 || args[0] != internalRepairHelperCommand {
		return false
	}
	if err := runInternalRepairHelper(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "internal Windows sandbox repair helper failed: %v\n", err)
		os.Exit(1)
	}
	return true
}
