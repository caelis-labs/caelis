package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/plugin"
)

const MaxHookOutputBytes = 32768

type maxBytesWriter struct {
	w         io.Writer
	limit     int
	written   int
	truncated bool
}

func (mbw *maxBytesWriter) Write(p []byte) (n int, err error) {
	// After the byte limit is reached, pretend writes still succeed so the
	// subprocess cannot block on a full stdout/stderr pipe.
	if mbw.written >= mbw.limit {
		mbw.truncated = true
		return len(p), nil
	}
	remaining := mbw.limit - mbw.written
	if len(p) > remaining {
		n, err = mbw.w.Write(p[:remaining])
		mbw.written += n
		mbw.truncated = true
		return len(p), err
	}
	n, err = mbw.w.Write(p)
	mbw.written += n
	return n, err
}

type HookStdin struct {
	Event        string             `json:"event"`
	PluginID     string             `json:"plugin_id"`
	SessionRef   session.SessionRef `json:"session_ref"`
	WorkspaceCWD string             `json:"workspace_cwd"`
	PluginDir    string             `json:"plugin_dir"`
}

// RunResult holds the outputs of a hook execution.
type RunResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	Error           error
	StdoutTruncated bool
	StderrTruncated bool
}

// Run executes a HookSpec with the provided context, session, and workspace directory.
func Run(ctx context.Context, spec plugin.HookSpec, sessionRef session.SessionRef, workspaceCWD string) RunResult {
	timeout := 10 * time.Second
	if spec.Timeout != "" {
		if d, err := time.ParseDuration(spec.Timeout); err == nil {
			timeout = d
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	compatEnv := map[string]string{
		"CAELIS_PLUGIN_DIR":    spec.PluginDir,
		"CAELIS_PLUGIN_ROOT":   spec.PluginDir,
		"CLAUDE_PLUGIN_DIR":    spec.PluginDir,
		"CLAUDE_PLUGIN_ROOT":   spec.PluginDir,
		"CODEX_PLUGIN_DIR":     spec.PluginDir,
		"CODEX_PLUGIN_ROOT":    spec.PluginDir,
		"CAELIS_WORKSPACE_DIR": workspaceCWD,
		"CLAUDE_WORKSPACE_DIR": workspaceCWD,
		"CODEX_WORKSPACE_DIR":  workspaceCWD,
	}

	expand := func(value string) string {
		env := map[string]string{}
		for k, v := range compatEnv {
			env[k] = v
		}
		for k, v := range spec.Env {
			env[k] = v
		}
		return os.Expand(value, func(key string) string {
			return env[key]
		})
	}

	cmdName := expand(spec.Command)
	cmdArgs := make([]string, 0, len(spec.Args))
	for _, arg := range spec.Args {
		cmdArgs = append(cmdArgs, expand(arg))
	}

	if cmdName == "" {
		return RunResult{
			Error: fmt.Errorf("plugin hooks: command is empty"),
		}
	}

	cmdDir := ""
	if spec.WorkDir != "" {
		cmdDir = expand(spec.WorkDir)
	} else if spec.PluginDir != "" {
		cmdDir = spec.PluginDir
	}

	cmdEnv := os.Environ()
	for k, v := range compatEnv {
		if v != "" {
			cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
		}
	}
	for k, v := range spec.Env {
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, expand(v)))
	}

	stdinPayload := HookStdin{
		Event:        string(spec.Event),
		PluginID:     spec.PluginID,
		SessionRef:   sessionRef,
		WorkspaceCWD: workspaceCWD,
		PluginDir:    spec.PluginDir,
	}

	stdinBytes, err := json.Marshal(stdinPayload)
	if err != nil {
		return RunResult{
			Error: fmt.Errorf("plugin hooks: failed to marshal stdin: %w", err),
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutLimitWriter := &maxBytesWriter{w: &stdoutBuf, limit: MaxHookOutputBytes}
	stderrLimitWriter := &maxBytesWriter{w: &stderrBuf, limit: MaxHookOutputBytes}

	newCmd := func(name string, args []string) *exec.Cmd {
		cmd := exec.CommandContext(runCtx, name, args...)
		if cmdDir != "" {
			cmd.Dir = cmdDir
		}
		cmd.Env = cmdEnv
		cmd.Stdin = bytes.NewReader(stdinBytes)
		cmd.Stdout = stdoutLimitWriter
		cmd.Stderr = stderrLimitWriter
		return cmd
	}

	cmd := newCmd(cmdName, cmdArgs)

	err = cmd.Run()
	if err != nil && runCtx.Err() == nil && runtime.GOOS != "windows" && isExecFormatError(err) {
		stdoutBuf.Reset()
		stderrBuf.Reset()
		stdoutLimitWriter = &maxBytesWriter{w: &stdoutBuf, limit: MaxHookOutputBytes}
		stderrLimitWriter = &maxBytesWriter{w: &stderrBuf, limit: MaxHookOutputBytes}
		err = newCmd("/bin/sh", append([]string{cmdName}, cmdArgs...)).Run()
	}
	if runCtx.Err() != nil {
		err = fmt.Errorf("plugin hooks: timeout after %s: %w", timeout, runCtx.Err())
	}

	var exitCode int
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return RunResult{
		Stdout:          stdoutBuf.String(),
		Stderr:          stderrBuf.String(),
		ExitCode:        exitCode,
		Error:           err,
		StdoutTruncated: stdoutLimitWriter.truncated,
		StderrTruncated: stderrLimitWriter.truncated,
	}
}

func isExecFormatError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.ENOEXEC)
}
