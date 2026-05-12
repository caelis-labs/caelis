package bootstrap

import "github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"

func MaybeRunInternalHelper(args []string) bool {
	return landlock.MaybeRunInternalHelper(args)
}
