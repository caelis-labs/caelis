package tuiapp

// cliputil.go owns WSL detection and clipboard utilities for the TUI surface.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/internal/winproc"
	"github.com/aymanbagabas/go-osc52/v2"
)

type clipboardCommand struct {
	name    string
	args    []string
	label   string
	timeout time.Duration
}

var (
	clipboardGOOS           = runtime.GOOS
	clipboardGetenv         = os.Getenv
	clipboardReadFile       = os.ReadFile
	clipboardRunCommand     = runClipboardCommand
	clipboardWriteWindows   = writeWindowsClipboardText
	clipboardOSC52Writer    io.Writer
	clipboardCommandTimeout = 2 * time.Second
	clipboardOpenTerminal   = func() (io.WriteCloser, error) {
		return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	}
)

func isWSL() bool {
	if strings.Contains(strings.ToLower(clipboardGetenv("WSL_DISTRO_NAME")), "wsl") {
		return true
	}
	if strings.Contains(strings.ToLower(clipboardGetenv("WSL_INTEROP")), "wsl") {
		return true
	}
	data, err := clipboardReadFile("/proc/version")
	if err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft") {
		return true
	}
	return false
}

func defaultReadClipboardText() (string, error) {
	if isWSL() {
		return wslReadText()
	}
	return nativeReadText()
}

func defaultWriteClipboardText(text string) error {
	if isWSL() {
		return wslWriteText(text)
	}
	return nativeWriteText(text)
}

func nativeReadText() (string, error) {
	cmds := readClipboardCommands()
	if len(cmds) == 0 {
		return "", fmt.Errorf("clipboard read is unsupported on %s", clipboardGOOS)
	}

	var errs []error
	for _, spec := range cmds {
		out, err := runClipboardOutputCommand(spec)
		if err == nil {
			return strings.TrimRight(string(out), "\r\n"), nil
		}
		errs = append(errs, err)
	}
	return "", joinClipboardErrors(errs)
}

func nativeWriteText(text string) error {
	if shouldPreferTerminalClipboard() {
		return writeOSC52ClipboardText(text)
	}
	if clipboardGOOS == "windows" {
		// clip.exe decodes stdin through the active code page; use CF_UNICODETEXT.
		return clipboardWriteWindows(text)
	}

	var errs []error
	for _, spec := range writeClipboardCommands() {
		err := clipboardRunCommand(spec, text)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}

	if err := writeOSC52ClipboardText(text); err != nil {
		errs = append(errs, err)
		return joinClipboardErrors(errs)
	}
	return nil
}

func readClipboardCommands() []clipboardCommand {
	switch clipboardGOOS {
	case "darwin":
		return []clipboardCommand{{name: "pbpaste"}}
	case "windows":
		return []clipboardCommand{{name: "powershell.exe", args: []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"}}}
	case "linux":
		cmds := make([]clipboardCommand, 0, 3)
		if clipboardGetenv("WAYLAND_DISPLAY") != "" {
			cmds = append(cmds, clipboardCommand{name: "wl-paste", args: []string{"--no-newline"}})
		}
		cmds = append(cmds,
			clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard", "-o"}},
			clipboardCommand{name: "xsel", args: []string{"--clipboard", "--output"}},
		)
		return cmds
	default:
		return nil
	}
}

func writeClipboardCommands() []clipboardCommand {
	switch clipboardGOOS {
	case "darwin":
		return []clipboardCommand{{name: "pbcopy"}}
	case "windows":
		return []clipboardCommand{{name: "clip.exe"}}
	case "linux":
		cmds := make([]clipboardCommand, 0, 3)
		if clipboardGetenv("WAYLAND_DISPLAY") != "" {
			cmds = append(cmds, clipboardCommand{name: "wl-copy"})
		}
		cmds = append(cmds,
			clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard"}},
			clipboardCommand{name: "xsel", args: []string{"--clipboard", "--input"}},
		)
		return cmds
	default:
		return nil
	}
}

func runClipboardCommand(spec clipboardCommand, input string) error {
	timeout := clipboardCommandTimeoutFor(spec)
	ctx, cancel := clipboardCommandContext(timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	winproc.ConfigureHiddenConsole(cmd)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return formatClipboardCommandError(spec, stderr.String(), fmt.Errorf("timed out after %s", timeout))
		}
		return formatClipboardCommandError(spec, stderr.String(), err)
	}
	return nil
}

func runClipboardOutputCommand(spec clipboardCommand) ([]byte, error) {
	timeout := clipboardCommandTimeoutFor(spec)
	ctx, cancel := clipboardCommandContext(timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	winproc.ConfigureHiddenConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, formatClipboardCommandError(spec, stderr.String(), fmt.Errorf("timed out after %s", timeout))
		}
		return nil, formatClipboardCommandError(spec, stderr.String(), err)
	}
	return out, nil
}

func clipboardCommandTimeoutFor(spec clipboardCommand) time.Duration {
	if spec.timeout > 0 {
		return spec.timeout
	}
	return clipboardCommandTimeout
}

func clipboardCommandContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

func formatClipboardCommandError(spec clipboardCommand, stderr string, err error) error {
	detail := strings.TrimSpace(stderr)
	if detail != "" {
		return fmt.Errorf("%s: %s: %w", spec.String(), detail, err)
	}
	return fmt.Errorf("%s: %w", spec.String(), err)
}

func (c clipboardCommand) String() string {
	if strings.TrimSpace(c.label) != "" {
		return strings.TrimSpace(c.label)
	}
	if len(c.args) == 0 {
		return c.name
	}
	return c.name + " " + strings.Join(c.args, " ")
}

func joinClipboardErrors(errs []error) error {
	joined := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			joined = append(joined, err)
		}
	}
	return errors.Join(joined...)
}

func shouldPreferTerminalClipboard() bool {
	return clipboardGetenv("SSH_CONNECTION") != "" || clipboardGetenv("SSH_CLIENT") != "" || clipboardGetenv("SSH_TTY") != ""
}

func writeOSC52ClipboardText(text string) error {
	seq := osc52.New(text)
	if clipboardGetenv("STY") != "" && clipboardGetenv("TMUX") == "" {
		seq = seq.Screen()
	}

	out := clipboardOSC52Writer
	if out == nil {
		terminal, err := clipboardOpenTerminal()
		if err == nil {
			defer terminal.Close()
			out = terminal
		} else {
			out = os.Stderr
		}
	}
	if _, err := seq.WriteTo(out); err != nil {
		return fmt.Errorf("osc52 clipboard: %w", err)
	}
	return nil
}

func wslReadText() (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "Get-Clipboard")
	winproc.ConfigureHiddenConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func wslWriteText(text string) error {
	cmd := exec.Command("clip.exe")
	winproc.ConfigureHiddenConsole(cmd)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
