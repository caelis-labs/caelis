//go:build linux

package bootstrap

import "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/landlock"

func MaybeRunInternalHelper(args []string) bool {
	return landlock.MaybeRunInternalHelper(args)
}
