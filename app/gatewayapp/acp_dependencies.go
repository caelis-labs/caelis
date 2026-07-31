package gatewayapp

import (
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type ACPAgentDependencies struct {
	Runtime     agent.Runtime
	Sessions    session.Service
	Assembly    assembly.ResolvedAssembly
	AppName     string
	UserID      string
	TaskStreams taskstream.Service
}

func (s *Stack) ACPAgentDependencies() (ACPAgentDependencies, error) {
	if s == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	deps := ACPAgentDependencies{
		Runtime:     s.engine,
		Sessions:    s.Sessions,
		Assembly:    s.runtime.Assembly,
		AppName:     s.AppName,
		UserID:      s.UserID,
		TaskStreams: s.taskStreams,
	}
	s.mu.RUnlock()
	if deps.Runtime == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return deps, nil
}
