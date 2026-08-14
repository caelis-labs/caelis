package runtime

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestNormalizeTaskControlRequestPreservesAppendNewline(t *testing.T) {
	exact := false
	normalized := normalizeTaskControlRequest(taskapi.ControlRequest{AppendNewline: &exact})
	if normalized.AppendNewline == nil || *normalized.AppendNewline {
		t.Fatalf("AppendNewline = %#v, want false", normalized.AppendNewline)
	}
	exact = true
	if *normalized.AppendNewline {
		t.Fatal("AppendNewline aliases caller pointer, want isolated clone")
	}
}

func TestNormalizeTaskWriteInputUsesTerminalLineEndingAndExactMode(t *testing.T) {
	exact := false
	appendLine := true
	for _, test := range []struct {
		name          string
		input         string
		appendNewline *bool
		backend       sandbox.Backend
		want          string
	}{
		{name: "unix default line", input: "demo", backend: sandbox.BackendSeatbelt, want: "demo\n"},
		{name: "unix explicit line", input: "demo", appendNewline: &appendLine, backend: sandbox.BackendBwrap, want: "demo\n"},
		{name: "windows default line", input: "demo", backend: sandbox.BackendWindows, want: "demo\r"},
		{name: "windows replaces line feed", input: "demo\n", backend: sandbox.BackendWindows, want: "demo\r"},
		{name: "windows collapses crlf", input: "demo\r\n", backend: sandbox.BackendWindows, want: "demo\r"},
		{name: "already terminated", input: "demo\r", backend: sandbox.BackendWindows, want: "demo\r"},
		{name: "exact escape", input: "\x1b[A", appendNewline: &exact, backend: sandbox.BackendWindows, want: "\x1b[A"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeTaskWriteInput(test.input, test.appendNewline, test.backend); got != test.want {
				t.Fatalf("normalizeTaskWriteInput() = %q, want %q", got, test.want)
			}
		})
	}
}
