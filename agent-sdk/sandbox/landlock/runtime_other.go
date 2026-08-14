//go:build !linux

package landlock

func MaybeRunInternalHelper(args []string) bool {
	_ = args
	return false
}
