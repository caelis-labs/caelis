//go:build windows

package conpty

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestConsoleSizeDefaultsAndClamps(t *testing.T) {
	got := consoleSize(0, 0)
	if got.X != 80 || got.Y != 24 {
		t.Fatalf("consoleSize(0,0) = %+v, want 80x24", got)
	}
	got = consoleSize(40000, 50000)
	if got.X != 32767 || got.Y != 32767 {
		t.Fatalf("consoleSize(max) = %+v, want clamped 32767x32767", got)
	}
}

func TestEnvironmentBlockHasDoubleNULTerminator(t *testing.T) {
	block, err := environmentBlock([]string{"A=1", "", "B=2"})
	if err != nil {
		t.Fatalf("environmentBlock() error = %v", err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatalf("environmentBlock() = %#v, want double NUL terminator", block)
	}
}

func TestStartCmdCapturesOutput(t *testing.T) {
	session, err := Start(Config{
		Command: "cmd.exe",
		Args:    []string{"/d", "/s", "/c", "echo conpty-ok"},
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(session.Output())
		output <- string(data)
	}()
	exitCode, err := session.Wait()
	_ = session.CloseConsole()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	select {
	case got := <-output:
		if !strings.Contains(got, "conpty-ok") {
			t.Fatalf("output = %q, want conpty-ok", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ConPTY output")
	}
}
