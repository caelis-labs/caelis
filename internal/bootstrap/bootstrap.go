package bootstrap

import (
	"github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"
	windowssandbox "github.com/OnslaughtSnail/caelis/impl/sandbox/windows"
)

func MaybeRunInternalHelper(args []string) bool {
	return landlock.MaybeRunInternalHelper(args) || windowssandbox.MaybeRunInternalHelper(args)
}
