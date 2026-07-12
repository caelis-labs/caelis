package builtin

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

func isReservedCoreToolName(name string) bool {
	_, ok := names.LookupExecutable(name)
	return ok
}

// CoreToolsConfig configures default core coding tools.
type CoreToolsConfig struct {
	Runtime      sandbox.Runtime
	Read         filesystem.ReadConfig
	SkillLoader  skill.Loader
	SkillCatalog skill.Catalog
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
	skillTool := skilltool.New(skilltool.Config{
		Loader:  cfg.SkillLoader,
		Catalog: cfg.SkillCatalog,
	})
	webSearchTool := web.NewSearch()
	webFetchTool, err := web.NewFetch(web.FetchConfig{})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{
		readTool, writeTool, patchTool, globTool, searchTool, runCommandTool, taskTool, planTool,
		skillTool, webSearchTool, webFetchTool,
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
