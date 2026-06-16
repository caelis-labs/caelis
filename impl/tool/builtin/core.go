package builtin

import (
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func isReservedCoreToolName(name string) bool {
	switch strings.TrimSpace(strings.ToUpper(name)) {
	case filesystem.ReadToolName, filesystem.WriteToolName, filesystem.PatchToolName,
		filesystem.ListToolName, filesystem.GlobToolName, filesystem.SearchToolName, shell.RunCommandToolName, task.ToolName, plan.ToolName:
		return true
	default:
		return false
	}
}

// CoreToolsConfig configures default core coding tools.
type CoreToolsConfig struct {
	Runtime sandbox.Runtime
	Read    filesystem.ReadConfig
}

// BuildCoreTools constructs the default coding tool group for the new SDK.
func BuildCoreTools(cfg CoreToolsConfig) ([]tool.Tool, error) {
	readTool, err := filesystem.NewRead(cfg.Read, cfg.Runtime)
	if err != nil {
		return nil, err
	}
	writeTool, err := filesystem.NewWrite(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	patchTool, err := filesystem.NewPatch(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	listTool, err := filesystem.NewList(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	globTool, err := filesystem.NewGlob(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	searchTool, err := filesystem.NewSearch(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	runCommandTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: cfg.Runtime})
	if err != nil {
		return nil, err
	}
	taskTool := task.New()
	planTool := plan.New()
	return []tool.Tool{
		readTool, writeTool, patchTool, listTool, globTool, searchTool, runCommandTool, taskTool, planTool,
	}, nil
}

// EnsureCoreTools injects default coding tools while rejecting user overrides
// of reserved builtin names.
func EnsureCoreTools(userTools []tool.Tool, builtins []tool.Tool) ([]tool.Tool, error) {
	filtered := make([]tool.Tool, 0, len(userTools))
	for _, one := range userTools {
		if one == nil {
			continue
		}
		if isReservedCoreToolName(one.Definition().Name) {
			return nil, fmt.Errorf("tool/builtin: %q is reserved by the core runtime and cannot be overridden", one.Definition().Name)
		}
		filtered = append(filtered, one)
	}
	out := make([]tool.Tool, 0, len(builtins)+len(filtered))
	out = append(out, builtins...)
	out = append(out, filtered...)
	return out, nil
}
