//go:build windows

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
)

func mcpTestConsoleWindow() uintptr {
	window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	return window
}

func TestMCPStdioWindowsServerHelper(t *testing.T) {
	if os.Getenv("CAELIS_MCP_WINDOWS_PROCESS") != "server" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-console-fixture", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		data, err := json.Marshal(map[string]any{"console": mcpTestConsoleWindow() != 0, "arguments": req.Params.Arguments})
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(data)}}}, err
	})
	_, _ = fmt.Fprintln(os.Stderr, "stdio-diagnostic-marker")
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPStdioWindowsNoConsole(t *testing.T) {
	if os.Getenv("CAELIS_MCP_WINDOWS_PROCESS") != "host" {
		// Match the managed Host's detached console state without changing the
		// test runner's own console or the user's TUI.
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
		defer cancel()
		parent := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPStdioWindowsNoConsole$", "-test.v")
		parent.Env = append(os.Environ(), "CAELIS_MCP_WINDOWS_PROCESS=host")
		parent.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
		out, err := parent.CombinedOutput()
		if err != nil {
			t.Fatalf("detached Host fixture: %v\n%s", err, out)
		}
		t.Logf("detached Host fixture:\n%s", out)
		return
	}
	if mcpTestConsoleWindow() != 0 {
		t.Fatal("Host fixture has a console")
	}
	for _, name := range []string{"legacy-control", "first-start", "restart-and-cancel"} {
		t.Run(name, func(t *testing.T) {
			transport, _, err := transportForSpecWithHTTPClient(ServerSpec{
				PluginID: "fixture", Name: "stdio", Command: os.Args[0], WorkDir: t.TempDir(),
				Args: []string{"-test.run=^TestMCPStdioWindowsServerHelper$"},
				Env:  map[string]string{"CAELIS_MCP_WINDOWS_PROCESS": "server"},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			command := transport.(*mcpsdk.CommandTransport).Command
			if name == "legacy-control" {
				// Observe allocation without CREATE_NO_WINDOW, but keep the
				// control's window hidden even on an interactive Windows machine.
				command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			}
			var diagnostics bytes.Buffer
			command.Stderr = &diagnostics
			lifetime, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "caelis-test", Version: "1"}, nil)
			session, err := connectWithTimeout(t.Context(), client, lifetime, cancel, transport, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(command.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process)
			listed, err := session.ListTools(t.Context(), nil)
			if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "probe" {
				t.Fatalf("stdio tool listing = %+v, %v", listed, err)
			}
			result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "probe", Arguments: map[string]any{"value": name}})
			if err != nil || result.IsError || len(result.Content) != 1 {
				t.Fatalf("stdio round trip = %+v, %v", result, err)
			}
			var observed struct {
				Console   bool              `json:"console"`
				Arguments map[string]string `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(result.Content[0].(*mcpsdk.TextContent).Text), &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Console != (name == "legacy-control") || observed.Arguments["value"] != name {
				t.Fatalf("child console/stdio = %+v", observed)
			}
			if name == "restart-and-cancel" {
				cancel()
			}
			_ = session.Close()
			status, err := windows.WaitForSingleObject(process, 5000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatalf("child not reaped after close/cancel: status=%d, err=%v", status, err)
			}
			if !strings.Contains(diagnostics.String(), "stdio-diagnostic-marker") || mcpTestConsoleWindow() != 0 {
				t.Fatal("stderr diagnostics or parent console state changed")
			}
			t.Logf("console=%v; stdio/stderr preserved; process reaped", observed.Console)
		})
	}
}
