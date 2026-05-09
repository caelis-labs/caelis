package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OnslaughtSnail/caelis/internal/bootstrap"
	"github.com/OnslaughtSnail/caelis/internal/cli"
)

func main() {
	if bootstrap.MaybeRunInternalHelper(os.Args[1:]) {
		return
	}
	if err := cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
