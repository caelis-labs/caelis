package tuiapp

// cliputil.go transplants WSL detection and clipboard utilities from the
// legacy internal/cli/cliputil package.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/aymanbagabas/go-osc52/v2"
)

type clipboardCommand struct {
	name string
	args []string
}

var (
	clipboardGOOS         = runtime.GOOS
	clipboardGetenv       = os.Getenv
	clipboardReadFile     = os.ReadFile
	clipboardRunCommand   = runClipboardCommand
	clipboardOSC52Writer  io.Writer
	clipboardOpenTerminal = func() (io.WriteCloser, error) {
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
			return strings.TrimRight(string(out), "\n"), nil
		}
		errs = append(errs, err)
	}
	return "", joinClipboardErrors(errs)
}

func nativeWriteText(text string) error {
	if shouldPreferTerminalClipboard() {
		return writeOSC52ClipboardText(text)
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
	cmd := exec.Command(spec.name, spec.args...)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return formatClipboardCommandError(spec, stderr.String(), err)
	}
	return nil
}

func runClipboardOutputCommand(spec clipboardCommand) ([]byte, error) {
	cmd := exec.Command(spec.name, spec.args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, formatClipboardCommandError(spec, stderr.String(), err)
	}
	return out, nil
}

func formatClipboardCommandError(spec clipboardCommand, stderr string, err error) error {
	detail := strings.TrimSpace(stderr)
	if detail != "" {
		return fmt.Errorf("%s: %s: %w", spec.String(), detail, err)
	}
	return fmt.Errorf("%s: %w", spec.String(), err)
}

func (c clipboardCommand) String() string {
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
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func wslWriteText(text string) error {
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
