package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OnslaughtSnail/caelis/internal/cli"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/landlock"
)

func main() {
	if landlock.MaybeRunInternalHelper(os.Args[1:]) {
		return
	}
	if err := cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
