package controladapter

import (
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

var (
	_ controlprompt.Service                   = (*Adapter)(nil)
	_ controlagents.Connector                 = (*Adapter)(nil)
	_ controlagents.Disconnector              = (*Adapter)(nil)
	_ agentbinding.Service                    = (*Adapter)(nil)
	_ controlprompt.StatusService             = (*Adapter)(nil)
	_ controlprompt.TurnService               = (*Adapter)(nil)
	_ controlprompt.SessionService            = (*Adapter)(nil)
	_ controlprompt.SessionModeService        = (*Adapter)(nil)
	_ controlprompt.ModelService              = (*Adapter)(nil)
	_ controlprompt.SandboxService            = (*Adapter)(nil)
	_ controlprompt.AgentService              = (*Adapter)(nil)
	_ controlprompt.ReviewService             = (*Adapter)(nil)
	_ controlprompt.CompletionService         = (*Adapter)(nil)
	_ controlprompt.SkillResolver             = (*Adapter)(nil)
	_ controlprompt.PluginService             = (*Adapter)(nil)
	_ controlprompt.LightweightStatusProvider = (*Adapter)(nil)
	_ controlprompt.Service                   = (*SessionClientAdapter)(nil)
	_ controlprompt.SkillResolver             = (*SessionClientAdapter)(nil)
	_ controlprompt.LightweightStatusProvider = (*SessionClientAdapter)(nil)
	_ controlagents.Connector                 = (*SessionClientAdapter)(nil)
	_ controlagents.Disconnector              = (*SessionClientAdapter)(nil)
	_ agentbinding.ConfigurationService       = (*SessionClientAdapter)(nil)
)
