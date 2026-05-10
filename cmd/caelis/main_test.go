package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestRunMainUsesInternalHelperBeforeCLI(t *testing.T) {
	calledCLI := false
	code := runMain(
		context.Background(),
		[]string{"internal-helper"},
		nil,
		io.Discard,
		io.Discard,
		func(args []string) bool {
			return reflect.DeepEqual(args, []string{"internal-helper"})
		},
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			calledCLI = true
			return nil
		},
	)
	if code != 0 {
		t.Fatalf("runMain() code = %d, want 0", code)
	}
	if calledCLI {
		t.Fatal("CLI runner was called after internal helper handled the invocation")
	}
}

func TestRunMainReportsCLIError(t *testing.T) {
	var stderr bytes.Buffer
	code := runMain(
		context.Background(),
		[]string{"--bad"},
		nil,
		io.Discard,
		&stderr,
		func([]string) bool { return false },
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("boom")
		},
	)
	if code != 1 {
		t.Fatalf("runMain() code = %d, want 1", code)
	}
	if stderr.String() != "boom\n" {
		t.Fatalf("stderr = %q, want boom newline", stderr.String())
	}
}
