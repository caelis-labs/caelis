package toolset

import (
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type SandboxConfig struct {
	CWD              string
	RequestedBackend string
	HelperPath       string
	ReadableRoots    []string
	WritableRoots    []string
	ReadOnlySubpaths []string
}

func NewSandboxRuntime(cfg SandboxConfig) (sandbox.Runtime, error) {
	return sandbox.New(sandbox.Config{
		CWD:              cfg.CWD,
		RequestedBackend: sandbox.Backend(cfg.RequestedBackend),
		HelperPath:       cfg.HelperPath,
		ReadableRoots:    append([]string(nil), cfg.ReadableRoots...),
		WritableRoots:    append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), cfg.ReadOnlySubpaths...),
	})
}

func BuildCoreTools(runtime sandbox.Runtime) ([]tool.Tool, error) {
	return builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: runtime})
}

func SpawnTool(agents []delegation.Agent) tool.Tool {
	return spawn.New(agents)
}

func SpawnTools(agents []delegation.Agent) []tool.Tool {
	if len(agents) == 0 {
		return nil
	}
	return []tool.Tool{SpawnTool(agents)}
}
