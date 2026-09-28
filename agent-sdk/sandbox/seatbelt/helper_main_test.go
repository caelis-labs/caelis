//go:build darwin

package seatbelt

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
