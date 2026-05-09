package bootstrap

import "github.com/OnslaughtSnail/caelis/sdk/sandbox/landlock"

func MaybeRunInternalHelper(args []string) bool {
	return landlock.MaybeRunInternalHelper(args)
}
