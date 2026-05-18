package main

import (
	"os"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/setupcmd"
)

func main() {
	if code := setupcmd.Run(os.Args[1:], os.Stderr); code != 0 {
		os.Exit(code)
	}
}
