//go:build windows

package bootstrap

import windowssandbox "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/windows"

func MaybeRunInternalHelper(args []string) bool {
	return windowssandbox.MaybeRunInternalHelper(args)
}
