//go:build linux

package bootstrap

func MaybeRunInternalHelper(args []string) bool {
	_ = args
	return false
}
