//go:build windows

package windows

func MaybeRunInternalHelper(args []string) bool {
	_ = args
	return false
}
