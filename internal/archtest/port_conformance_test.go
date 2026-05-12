package archtest

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/config"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/prompt"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	taskport "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/controller"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/approval/agentreview"
	"github.com/OnslaughtSnail/caelis/impl/approval/deny"
	"github.com/OnslaughtSnail/caelis/impl/approval/manual"
	configfile "github.com/OnslaughtSnail/caelis/impl/config/file"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/prompt/static"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/impl/skill/fs"
	"github.com/OnslaughtSnail/caelis/impl/stream/memory"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	tooltask "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
)

func TestPortImplementationConformance(t *testing.T) {
	t.Parallel()
}

var (
	_ agent.Runtime      = (*local.Runtime)(nil)
	_ controller.Backend = (*acp.Manager)(nil)

	_ approval.Approver = deny.Approver{}
	_ approval.Approver = manual.Approver{}
	_ approval.Approver = agentreview.Approver{}

	_ config.Store = (*configfile.Store)(nil)

	_ model.Provider = providers.Provider{}
	_ model.Registry = providers.Registry{}

	_ policy.Mode = presets.AutoReviewMode()
	_ policy.Mode = presets.ManualMode()

	_ prompt.Assembler        = static.Assembler{}
	_ prompt.FragmentProvider = static.FragmentProvider{}

	_ sandbox.Runtime = (*host.Runtime)(nil)

	_ session.Store   = (*sessionfile.Store)(nil)
	_ session.Store   = (*inmemory.Store)(nil)
	_ session.Service = (*sessionfile.Service)(nil)
	_ session.Service = (*inmemory.Service)(nil)

	_ skill.Discovery = fs.Discovery{}
	_ skill.Loader    = fs.Loader{}

	_ stream.Service    = (*memory.Service)(nil)
	_ stream.Sink       = (*memory.Service)(nil)
	_ stream.Controller = (*memory.Service)(nil)

	_ taskport.Store = (*taskfile.Store)(nil)

	_ tool.Tool = (*filesystem.ReadTool)(nil)
	_ tool.Tool = (*filesystem.WriteTool)(nil)
	_ tool.Tool = (*filesystem.PatchTool)(nil)
	_ tool.Tool = (*filesystem.GlobTool)(nil)
	_ tool.Tool = (*filesystem.ListTool)(nil)
	_ tool.Tool = (*filesystem.SearchTool)(nil)
	_ tool.Tool = (*shell.BashTool)(nil)
	_ tool.Tool = plan.New()
	_ tool.Tool = tooltask.New()
	_ tool.Tool = spawn.New(nil)
)
