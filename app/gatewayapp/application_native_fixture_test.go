//go:build darwin || linux || windows

package gatewayapp_test

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func nativeCommandTool(t *testing.T, command string) nativeModelTool {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return nativeModelTool{"RunCommand", string(raw)}
}

func nativeCopyCommand(inputs []string, output string) string {
	parts := make([]string, len(inputs))
	if runtime.GOOS == "windows" {
		for i, input := range inputs {
			parts[i] = "[IO.File]::ReadAllText('" + strings.ReplaceAll(input, "'", "''") + "')"
		}
		return "$ErrorActionPreference='Stop'; [IO.File]::WriteAllText('" + strings.ReplaceAll(output, "'", "''") + "', (" + strings.Join(parts, "+") + "))"
	}
	for i, input := range inputs {
		parts[i] = "'" + strings.ReplaceAll(input, "'", "'\\''") + "'"
	}
	return "cat " + strings.Join(parts, " ") + " > '" + strings.ReplaceAll(output, "'", "'\\''") + "'"
}
