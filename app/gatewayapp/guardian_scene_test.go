package gatewayapp

import (
	"io"
	"log/slog"
	"testing"

	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestStackNewGuardianApproverUsesStackSessions(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	diagnostics := slog.New(slog.NewTextHandler(io.Discard, nil))
	stack := &Stack{composition: runtimeComposition{
		sessions: sessions,
		authorities: runtimeHostAuthorities{
			diagnostics: diagnostics,
		},
	}}

	approver := stack.composition.newGuardianApprover()
	if approver == nil {
		t.Fatal("newGuardianApprover() = nil")
	}
	if approver.sessions != sessions {
		t.Fatal("newGuardianApprover() did not preserve the Stack session service")
	}
	runner, ok := approver.systemAgents.(*systemManagedAgentRuntime)
	if !ok || runner.config.Diagnostics != diagnostics {
		t.Fatal("newGuardianApprover() did not preserve private Runtime diagnostics")
	}
}
