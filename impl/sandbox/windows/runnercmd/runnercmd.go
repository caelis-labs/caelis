package runnercmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/winps"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/conpty"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/job"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnerproto"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

func Run(stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	runner := &runner{
		reader: runnerproto.NewReader(stdin),
		writer: runnerproto.NewWriter(stdout),
		stderr: stderr,
	}
	if err := runner.run(); err != nil {
		fmt.Fprintln(stderr, err)
		_ = runner.writeFrame(runnerproto.TypeError, runnerproto.Error{Phase: "runner", Message: err.Error()})
		return 1
	}
	return 0
}

type runner struct {
	reader *runnerproto.Reader
	writer *runnerproto.Writer
	stderr io.Writer
	mu     sync.Mutex
}

func (r *runner) run() error {
	if err := r.writeFrame(runnerproto.TypeHello, runnerproto.Hello{
		RunnerVersion: "dev",
		Identity:      os.Getenv("USERNAME"),
		Capabilities:  []string{"non_tty", "conpty", "resize", "stdin", "stdout", "stderr", "timeout", "kill", "restricted_token", "capability_restricted_sid"},
	}); err != nil {
		return err
	}
	frame, err := r.reader.ReadFrame()
	if err != nil {
		return err
	}
	if frame.Type != runnerproto.TypeSpawn {
		return fmt.Errorf("command runner: first frame must be spawn, got %q", frame.Type)
	}
	var spawn runnerproto.Spawn
	if err := frame.DecodePayload(&spawn); err != nil {
		return fmt.Errorf("decode spawn: %w", err)
	}
	return r.runSpawn(spawn)
}

func (r *runner) runSpawn(spawn runnerproto.Spawn) error {
	if strings.TrimSpace(spawn.Command) == "" {
		return fmt.Errorf("command runner: command is required")
	}
	if len(spawn.CapabilitySID) == 0 {
		return fmt.Errorf("command runner: capability SIDs are required")
	}
	if spawn.TTY {
		return r.runTTY(spawn)
	}
	env, err := mergeEnv(spawn.Env, spawn.Network, spawn.CWD)
	if err != nil {
		return err
	}
	token, releaseToken, err := restrictedToken(spawn.CapabilitySID)
	if err != nil {
		return fmt.Errorf("command runner: restricted token: %w", err)
	}
	defer releaseToken()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return err
	}
	process, err := win32.StartProcessAsUser(token, powershell, powershellArgs(commandWithLocation(spawn.Command, spawn.CWD), false, spawn.StdinOpen), "", env)
	if err != nil {
		return err
	}
	jobObject, err := job.New()
	if err == nil {
		err = jobObject.AssignPID(process.PID())
	}
	if err != nil {
		_ = process.Kill()
		return fmt.Errorf("command runner: assign job object: %w", err)
	}
	defer jobObject.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go r.copyOutput(&wg, runnerproto.TypeStdout, process.Stdout())
	go r.copyOutput(&wg, runnerproto.TypeStderr, process.Stderr())
	go r.readControl(process, jobObject, process.Stdin())

	var timedOut atomic.Bool
	var timer *time.Timer
	if spawn.Timeout > 0 {
		timer = time.AfterFunc(spawn.Timeout, func() {
			timedOut.Store(true)
			_ = jobObject.Terminate(1)
			_ = process.Kill()
		})
		defer timer.Stop()
	}

	waitErr := process.Wait()
	waitOutput := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitOutput)
	}()
	select {
	case <-waitOutput:
	case <-time.After(2 * time.Second):
	}
	exitCode := 0
	reason := ""
	var exitErr win32.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode
	}
	if timedOut.Load() {
		reason = context.DeadlineExceeded.Error()
	}
	if waitErr != nil && reason == "" {
		reason = waitErr.Error()
	}
	return r.writeFrame(runnerproto.TypeExit, runnerproto.Exit{ExitCode: exitCode, Reason: reason})
}

func (r *runner) runSpawnAsCurrentUser(spawn runnerproto.Spawn, env []string) error {
	ctx := context.Background()
	cancel := func() {}
	if spawn.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spawn.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe", powershellArgs(commandWithLocation(spawn.Command, spawn.CWD), false, spawn.StdinOpen)...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	jobObject, err := job.New()
	if err == nil {
		err = jobObject.AssignPID(cmd.Process.Pid)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("command runner: assign job object: %w", err)
	}
	defer jobObject.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go r.copyOutput(&wg, runnerproto.TypeStdout, stdout)
	go r.copyOutput(&wg, runnerproto.TypeStderr, stderr)
	go r.readControl(execKillable{cmd: cmd}, jobObject, stdin)

	waitErr := cmd.Wait()
	_ = stdin.Close()
	wg.Wait()
	exitCode := 0
	reason := ""
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		reason = ctx.Err().Error()
	}
	if waitErr != nil && reason == "" {
		reason = waitErr.Error()
	}
	return r.writeFrame(runnerproto.TypeExit, runnerproto.Exit{ExitCode: exitCode, Reason: reason})
}

func (r *runner) runTTY(spawn runnerproto.Spawn) error {
	token, releaseToken, err := restrictedToken(spawn.CapabilitySID)
	if err != nil {
		return fmt.Errorf("command runner: restricted token: %w", err)
	}
	defer releaseToken()
	env, err := mergeEnv(spawn.Env, spawn.Network, spawn.CWD)
	if err != nil {
		return err
	}
	pty, err := conpty.Start(conpty.Config{
		Command: "powershell.exe",
		Args:    powershellArgs(commandWithLocation(spawn.Command, spawn.CWD), true, true),
		Env:     env,
		Rows:    spawn.Rows,
		Cols:    spawn.Cols,
		Token:   token,
	})
	if err != nil {
		return err
	}
	defer pty.Close()

	jobObject, err := job.New()
	if err == nil {
		err = jobObject.AssignPID(pty.PID())
	}
	if err != nil {
		_ = pty.Kill()
		return fmt.Errorf("command runner: assign job object: %w", err)
	}
	defer jobObject.Close()

	var timedOut atomic.Bool
	var timer *time.Timer
	if spawn.Timeout > 0 {
		timer = time.AfterFunc(spawn.Timeout, func() {
			timedOut.Store(true)
			_ = jobObject.Terminate(1)
			_ = pty.Kill()
		})
		defer timer.Stop()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go r.copyOutput(&wg, runnerproto.TypeStdout, pty.Output())
	go r.readTTYControl(pty, jobObject, pty.Input())

	exitCode, waitErr := pty.Wait()
	_ = pty.CloseConsole()
	wg.Wait()

	reason := ""
	if timedOut.Load() {
		reason = context.DeadlineExceeded.Error()
	}
	if waitErr != nil && reason == "" {
		reason = waitErr.Error()
	}
	return r.writeFrame(runnerproto.TypeExit, runnerproto.Exit{ExitCode: exitCode, Reason: reason})
}

func (r *runner) copyOutput(wg *sync.WaitGroup, typ string, reader io.Reader) {
	defer wg.Done()
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			_ = r.writeFrame(typ, runnerproto.Bytes{Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			return
		}
	}
}

type killableProcess interface {
	Kill() error
}

type execKillable struct {
	cmd *exec.Cmd
}

func (p execKillable) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (r *runner) readControl(process killableProcess, jobObject *job.Object, stdin io.WriteCloser) {
	defer stdin.Close()
	for {
		frame, err := r.reader.ReadFrame()
		if err != nil {
			return
		}
		switch frame.Type {
		case runnerproto.TypeStdin:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err == nil && len(payload.Data) > 0 {
				_, _ = stdin.Write(payload.Data)
			}
		case runnerproto.TypeStdinClose:
			return
		case runnerproto.TypeInterrupt, runnerproto.TypeKill:
			_ = jobObject.Terminate(1)
			_ = process.Kill()
			return
		}
	}
}

func (r *runner) readTTYControl(session *conpty.Session, jobObject *job.Object, stdin io.WriteCloser) {
	for {
		frame, err := r.reader.ReadFrame()
		if err != nil {
			return
		}
		switch frame.Type {
		case runnerproto.TypeStdin:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err == nil && len(payload.Data) > 0 {
				_, _ = stdin.Write(payload.Data)
			}
		case runnerproto.TypeStdinClose:
			_ = stdin.Close()
			return
		case runnerproto.TypeResize:
			var payload runnerproto.Resize
			if err := frame.DecodePayload(&payload); err == nil {
				_ = session.Resize(payload.Rows, payload.Cols)
			}
		case runnerproto.TypeInterrupt, runnerproto.TypeKill:
			_ = jobObject.Terminate(1)
			_ = session.Kill()
			_ = stdin.Close()
			return
		}
	}
}

func (r *runner) writeFrame(typ string, payload any) error {
	frame, err := runnerproto.NewFrame(typ, payload)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writer.WriteFrame(frame)
}

func mergeEnv(extra map[string]string, network string, cwd string) ([]string, error) {
	env := os.Environ()
	if strings.TrimSpace(cwd) != "" {
		home := filepath.Join(strings.TrimSpace(cwd), ".caelis-sandbox", "home")
		tmp := filepath.Join(strings.TrimSpace(cwd), ".caelis-sandbox", "tmp")
		localAppData := filepath.Join(home, "AppData", "Local")
		roamingAppData := filepath.Join(home, "AppData", "Roaming")
		for _, dir := range []string{home, tmp, localAppData, roamingAppData} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		env = append(env,
			"CAELIS_SANDBOX_HOME="+home,
			"USERPROFILE="+home,
			"HOME="+home,
			"TEMP="+tmp,
			"TMP="+tmp,
			"LOCALAPPDATA="+localAppData,
			"APPDATA="+roamingAppData,
		)
	}
	if strings.EqualFold(strings.TrimSpace(network), "offline") {
		env = append(env,
			"CAELIS_SANDBOX_NETWORK=disabled",
			"HTTP_PROXY=http://127.0.0.1:9",
			"HTTPS_PROXY=http://127.0.0.1:9",
			"ALL_PROXY=http://127.0.0.1:9",
			"NO_PROXY=localhost,127.0.0.1,::1",
		)
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env, nil
}

func powershellArgs(command string, tty bool, interactive bool) []string {
	return winps.Args(command, winps.Options{TTY: tty, Interactive: interactive})
}

func commandWithLocation(command string, cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return command
	}
	escaped := strings.ReplaceAll(cwd, "'", "''")
	return "Set-Location -LiteralPath '" + escaped + "'; " + command
}
