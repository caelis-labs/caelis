//go:build !linux && !windows && !darwin

package bootstrap

func MaybeRunInternalHelper(args []string) bool {
	_ = args
	return false
}
