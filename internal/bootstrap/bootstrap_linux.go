//go:build linux

package bootstrap

import "github.com/caelis-labs/caelis/agent-sdk/sandbox/landlock"

func MaybeRunInternalHelper(args []string) bool {
	return landlock.MaybeRunInternalHelper(args)
}
