package shell

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
)

func TestRunCommandToolExecutesThroughSandbox(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	input, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runTool.Call(context.Background(), tool.Call{ID: "call-1", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result IsError = true: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || !strings.Contains(result.Content[0].Text.Text, "hello") {
		t.Fatalf("result content = %#v, want stdout text", result.Content)
	}
}

func TestRunCommandToolReturnsModelVisibleCommandFailures(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"command": "exit 7"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result IsError = false, want true")
	}
	if got := result.Meta["exit_code"]; got != 7 {
		t.Fatalf("exit code meta = %v, want 7", got)
	}
}
