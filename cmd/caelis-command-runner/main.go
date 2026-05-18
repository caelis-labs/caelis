package main

import (
	"os"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/runnercmd"
)

func main() {
	if code := runnercmd.Run(os.Stdin, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}
