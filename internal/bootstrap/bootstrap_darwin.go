//go:build darwin

package bootstrap

import "github.com/caelis-labs/caelis/agent-sdk/sandbox/seatbelt"

func MaybeRunInternalHelper(args []string) bool {
	return seatbelt.MaybeRunInternalHelper(args)
}
