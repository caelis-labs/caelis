package tuiapp

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDefaultWriteClipboardTextUsesOSC52ForSSH(t *testing.T) {
	restore := stubClipboardEnv(t, map[string]string{
		"SSH_CONNECTION": "client server",
	})
	defer restore()

	var ran []string
	clipboardRunCommand = func(spec clipboardCommand, input string) error {
		ran = append(ran, spec.String())
		return nil
	}

	var out strings.Builder
	clipboardOSC52Writer = &out

	if err := defaultWriteClipboardText("hello"); err != nil {
		t.Fatalf("defaultWriteClipboardText returned error: %v", err)
	}
	if len(ran) != 0 {
		t.Fatalf("expected SSH path to skip local clipboard commands, ran %v", ran)
	}
	if got := out.String(); got != "\x1b]52;c;aGVsbG8=\x07" {
		t.Fatalf("unexpected OSC52 sequence %q", got)
	}
}

func TestDefaultWriteClipboardTextUsesOSC52ForMacSSH(t *testing.T) {
	restore := stubClipboardEnv(t, map[string]string{
		"SSH_TTY": "/dev/pts/1",
	})
	defer restore()
	clipboardGOOS = "darwin"

	clipboardRunCommand = func(spec clipboardCommand, input string) error {
		t.Fatalf("did not expect %s to run over SSH", spec)
		return nil
	}

	var out strings.Builder
	clipboardOSC52Writer = &out

	if err := defaultWriteClipboardText("mac"); err != nil {
		t.Fatalf("defaultWriteClipboardText returned error: %v", err)
	}
	if got := out.String(); got != "\x1b]52;c;bWFj\x07" {
		t.Fatalf("unexpected OSC52 sequence %q", got)
	}
}

func TestNativeWriteTextUsesWaylandBeforeX11(t *testing.T) {
	restore := stubClipboardEnv(t, map[string]string{
		"WAYLAND_DISPLAY": "wayland-0",
	})
	defer restore()
	clipboardGOOS = "linux"

	var ran []string
	clipboardRunCommand = func(spec clipboardCommand, input string) error {
		ran = append(ran, spec.String())
		if input != "hello" {
			t.Fatalf("unexpected command input %q", input)
		}
		return nil
	}

	if err := nativeWriteText("hello"); err != nil {
		t.Fatalf("nativeWriteText returned error: %v", err)
	}
	if got, want := strings.Join(ran, ","), "wl-copy"; got != want {
		t.Fatalf("commands ran in wrong order: got %q want %q", got, want)
	}
}

func TestNativeWriteTextFallsBackToOSC52WithCommandDiagnostics(t *testing.T) {
	restore := stubClipboardEnv(t, nil)
	defer restore()
	clipboardGOOS = "linux"

	clipboardRunCommand = func(spec clipboardCommand, input string) error {
		return errors.New(spec.String() + ": Can't open display")
	}
	clipboardOSC52Writer = failingWriter{}

	err := nativeWriteText("hello")
	if err == nil {
		t.Fatal("expected error")
	}
	text := err.Error()
	for _, want := range []string{
		"xclip -selection clipboard: Can't open display",
		"xsel --clipboard --input: Can't open display",
		"osc52 clipboard: tty closed",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected error to contain %q, got %q", want, text)
		}
	}
}

func TestOSC52UsesScreenWrapperOnlyForScreen(t *testing.T) {
	restore := stubClipboardEnv(t, map[string]string{
		"SSH_TTY": "/dev/pts/1",
		"STY":     "screen-session",
	})
	defer restore()

	var out strings.Builder
	clipboardOSC52Writer = &out

	if err := defaultWriteClipboardText("screen"); err != nil {
		t.Fatalf("defaultWriteClipboardText returned error: %v", err)
	}
	if got := out.String(); got != "\x1bP\x1b]52;c;c2NyZWVu\x07\x1b\\" {
		t.Fatalf("unexpected screen-wrapped OSC52 sequence %q", got)
	}
}

func stubClipboardEnv(t *testing.T, env map[string]string) func() {
	t.Helper()

	oldGOOS := clipboardGOOS
	oldGetenv := clipboardGetenv
	oldReadFile := clipboardReadFile
	oldRunCommand := clipboardRunCommand
	oldWriter := clipboardOSC52Writer
	oldOpenTerminal := clipboardOpenTerminal

	clipboardGOOS = "linux"
	clipboardGetenv = func(key string) string {
		return env[key]
	}
	clipboardReadFile = func(name string) ([]byte, error) {
		return []byte("Linux version"), nil
	}
	clipboardRunCommand = func(spec clipboardCommand, input string) error {
		t.Fatalf("unexpected clipboard command %s", spec)
		return nil
	}
	clipboardOSC52Writer = nil
	clipboardOpenTerminal = func() (io.WriteCloser, error) {
		return nil, errors.New("no tty")
	}

	return func() {
		clipboardGOOS = oldGOOS
		clipboardGetenv = oldGetenv
		clipboardReadFile = oldReadFile
		clipboardRunCommand = oldRunCommand
		clipboardOSC52Writer = oldWriter
		clipboardOpenTerminal = oldOpenTerminal
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("tty closed")
}
