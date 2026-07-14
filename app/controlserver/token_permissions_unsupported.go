//go:build !unix && !windows

package controlserver

import (
	"fmt"
	"os"
	"runtime"
)

func secureTokenFile(*os.File) error {
	return fmt.Errorf("secure token files are unsupported on %s", runtime.GOOS)
}

func validateTokenFileSecurity(*os.File, os.FileInfo) error {
	return fmt.Errorf("secure token files are unsupported on %s", runtime.GOOS)
}

func syncTokenDirectory(string) error {
	return fmt.Errorf("secure token files are unsupported on %s", runtime.GOOS)
}
