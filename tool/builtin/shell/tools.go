package shell

import (
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// runCommand implements the RUN_COMMAND tool.
// It receives a sandbox.Backend via tool context and executes commands.
type runCommand struct{}

func (*runCommand) Definition() tool.Definition {
	return tool.Definition{
		Name:        "RUN_COMMAND",
		Description: "Execute a shell command in the sandbox.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"command":             {Type: "string", Description: "Command to execute"},
				"workdir":             {Type: "string", Description: "Working directory"},
				"sandbox_permissions": {Type: "string", Description: "Sandbox permission level"},
			},
			Required: []string{"command"},
		},
		Metadata: map[string]any{
			"sandbox_aware": true,
		},
	}
}

func (*runCommand) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	command, _ := call.Args["command"].(string)
	if command == "" {
		return tool.Result{Output: "command is required", IsError: true}, nil
	}
	workdir, _ := call.Args["workdir"].(string)

	// Build command request.
	req := sandbox.CommandRequest{
		Command: command,
		Dir:     workdir,
		Timeout: 300, // 5 minute default
	}

	// Extract sandbox constraints from call metadata if available.
	if meta := call.Metadata; meta != nil {
		if timeout, ok := meta["timeout"].(int); ok && timeout > 0 {
			req.Timeout = timeout
		}
	}

	// Execute via sandbox backend.
	// The runner wires the sandbox filesystem into tool context.
	// For command execution, we need a sandbox.Backend, not just FileSystem.
	// The current architecture provides FileSystem via tool.Context.
	// Command execution happens through the host sandbox backend.
	result, err := executeCommand(ctx, req)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("execution error: %v", err), IsError: true}, nil
	}

	// Build output.
	var output strings.Builder
	if len(result.Stdout) > 0 {
		output.Write(result.Stdout)
	}
	if len(result.Stderr) > 0 {
		if output.Len() > 0 {
			output.WriteString("\n--- stderr ---\n")
		}
		output.Write(result.Stderr)
	}

	if result.ExitCode != 0 {
		return tool.Result{
			Output:  output.String(),
			IsError: true,
			Metadata: map[string]any{
				"exit_code": result.ExitCode,
			},
		}, nil
	}

	return tool.Result{Output: output.String()}, nil
}

// executeCommand runs a command using the sandbox backend.
// It tries to get a Backend from the tool context, falling back to host.
func executeCommand(ctx tool.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	// Use the sandbox backend if available through a BackendProvider.
	if bp, ok := ctx.(interface{ SandboxBackend() sandbox.Backend }); ok {
		if b := bp.SandboxBackend(); b != nil {
			return b.Run(ctx, req)
		}
	}

	// Fallback: execute directly via host.
	return executeHostCommand(ctx, req)
}

// All returns all shell built-in tools.
func All() []tool.Tool {
	return []tool.Tool{&runCommand{}}
}
