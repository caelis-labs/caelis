package builtin

import (
	"context"
	"fmt"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/shell"
	builtintask "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/task"
)

func isReservedCoreToolName(name string) bool {
	switch strings.TrimSpace(strings.ToUpper(name)) {
	case filesystem.ReadToolName, filesystem.WriteToolName, filesystem.PatchToolName,
		filesystem.ListToolName, filesystem.GlobToolName, filesystem.SearchToolName, shell.BashToolName, builtintask.ToolName, plan.ToolName:
		return true
	default:
		return false
	}
}

// CoreToolsConfig configures default core coding tools.
type CoreToolsConfig struct {
	Runtime sdksandbox.Runtime
	Read    filesystem.ReadConfig
}

// BuildCoreTools constructs the default coding tool group for the new SDK.
func BuildCoreTools(cfg CoreToolsConfig) ([]sdktool.Tool, error) {
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
	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: cfg.Runtime})
	if err != nil {
		return nil, err
	}
	taskTool := builtintask.New()
	planTool := plan.New()
	return []sdktool.Tool{
		readTool, writeTool, patchTool, listTool, globTool, searchTool, bashTool, taskTool, planTool,
	}, nil
}

// EnsureCoreTools injects default coding tools while rejecting user overrides
// of reserved builtin names.
func EnsureCoreTools(userTools []sdktool.Tool, builtins []sdktool.Tool) ([]sdktool.Tool, error) {
	filtered := make([]sdktool.Tool, 0, len(userTools))
	for _, one := range userTools {
		if one == nil {
			continue
		}
		if isReservedCoreToolName(one.Definition().Name) {
			return nil, fmt.Errorf("tool/builtin: %q is reserved by the core runtime and cannot be overridden", one.Definition().Name)
		}
		filtered = append(filtered, one)
	}
	out := make([]sdktool.Tool, 0, len(builtins)+len(filtered))
	out = append(out, builtins...)
	out = append(out, filtered...)
	return out, nil
}

// RunTool is one small helper for builtin end-to-end tests.
func RunTool(ctx context.Context, one sdktool.Tool, input string) (sdktool.Result, error) {
	return one.Call(ctx, sdktool.Call{Name: one.Definition().Name, Input: []byte(input)})
}
