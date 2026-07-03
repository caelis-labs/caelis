package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/caelis-labs/caelis/internal/bootstrap"
	"github.com/caelis-labs/caelis/internal/cli"
)

func main() {
	code := runMain(
		context.Background(),
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		bootstrap.MaybeRunInternalHelper,
		cli.Run,
	)
	if code != 0 {
		os.Exit(code)
	}
}

type helperRunner func([]string) bool
type cliRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer) error

func runMain(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	runHelper helperRunner,
	runCLI cliRunner,
) int {
	if runHelper != nil && runHelper(args) {
		return 0
	}
	if runCLI == nil {
		return 0
	}
	if err := runCLI(ctx, args, stdin, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
