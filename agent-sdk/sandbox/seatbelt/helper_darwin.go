//go:build darwin

package seatbelt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const seatbeltHelperCommand = "__caelis_seatbelt_execenv_helper__"
const seatbeltHelperReady = "caelis-seatbelt-helper-ready"

// MaybeRunInternalHelper handles Seatbelt's self-exec command before the host
// parses ordinary CLI flags. Embedders must invoke this in their process entry
// when using Seatbelt; Config.HelperPath may select another wired executable.
// If ResourceLimits.ReadPaths is explicit, that executable must be within it.
func MaybeRunInternalHelper(args []string) bool {
	if len(args) == 0 || args[0] != seatbeltHelperCommand {
		return false
	}
	if err := runSeatbeltHelper(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "internal sandbox helper failed: %v\n", err)
		os.Exit(1)
	}
	return true
}

func runSeatbeltHelper(args []string) error {
	fs := flag.NewFlagSet(seatbeltHelperCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var probe bool
	var shell, shellFlag, command string
	var envFD int
	fs.BoolVar(&probe, "probe", false, "probe helper")
	fs.StringVar(&shell, "shell", "", "target shell")
	fs.StringVar(&shellFlag, "shell-flag", "", "target shell flag")
	fs.StringVar(&command, "command", "", "target command")
	fs.IntVar(&envFD, "env-fd", -1, "target environment descriptor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if probe {
		fmt.Println(seatbeltHelperReady)
		return nil
	}
	if shell == "" || (shellFlag != "-c" && shellFlag != "-lc") || envFD < 0 {
		return errors.New("incomplete seatbelt helper command")
	}
	file := os.NewFile(uintptr(envFD), "target environment")
	if file == nil {
		return errors.New("target environment descriptor is unavailable")
	}
	payload, err := io.ReadAll(io.LimitReader(file, 8<<20))
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("read target environment: %w", err)
	}
	var targetEnv []string
	if err := json.Unmarshal(payload, &targetEnv); err != nil {
		return fmt.Errorf("decode target environment: %w", err)
	}
	return unix.Exec(shell, []string{shell, shellFlag, command}, targetEnv)
}

func (s *seatbeltRunner) probeHelper(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := s.execCommand(probeCtx, s.helperPath, seatbeltHelperCommand, "--probe")
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("seatbelt helper %q is not wired (call seatbelt.MaybeRunInternalHelper at process entry): %w", s.helperPath, err)
	}
	if strings.TrimSpace(stdout.String()) != seatbeltHelperReady || stderr.Len() != 0 {
		return fmt.Errorf("seatbelt helper %q did not report expected readiness (call seatbelt.MaybeRunInternalHelper at process entry)", s.helperPath)
	}
	return nil
}
