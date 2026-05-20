//go:build !linux && !windows

package bootstrap

func MaybeRunInternalHelper(args []string) bool {
	_ = args
	return false
}
