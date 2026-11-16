package sessionprofile

import "github.com/caelis-labs/caelis/agent-sdk/tool/codemode"

// NativeToolNames returns the default native filesystem and command selection
// for workspace-write profiles. An explicit empty selection disables these tools.
// The returned slice is independent; assembly retains SDK tool order and adds
// the resource bridge separately.
func NativeToolNames() []string {
	return []string{
		"Read", "ViewImage", "Write", "Patch", "Glob", "Grep", "RunCommand", "Task", codemode.ToolName,
	}
}
