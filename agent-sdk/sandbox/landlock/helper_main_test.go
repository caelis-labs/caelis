//go:build linux

package landlock

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if MaybeRunInternalHelper(os.Args[1:]) {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
