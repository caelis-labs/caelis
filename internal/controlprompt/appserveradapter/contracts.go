package appserveradapter

import (
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

var (
	_ controlprompt.RouterService             = (*SessionClientAdapter)(nil)
	_ controlprompt.SessionModeService        = (*SessionClientAdapter)(nil)
	_ controlprompt.CompletionService         = (*SessionClientAdapter)(nil)
	_ controlprompt.PluginService             = (*SessionClientAdapter)(nil)
	_ controlprompt.SkillResolver             = (*SessionClientAdapter)(nil)
	_ controlprompt.LightweightStatusProvider = (*SessionClientAdapter)(nil)
	_ controlagents.Connector                 = (*SessionClientAdapter)(nil)
	_ controlagents.Disconnector              = (*SessionClientAdapter)(nil)
	_ agentbinding.ConfigurationService       = (*SessionClientAdapter)(nil)
)
