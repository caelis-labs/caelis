package gatewayapp

import (
	"testing"

	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestStackNewGuardianApproverUsesStackSessions(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	stack := &Stack{Sessions: sessions}

	approver := stack.newGuardianApprover()
	if approver == nil {
		t.Fatal("newGuardianApprover() = nil")
	}
	if approver.sessions != sessions {
		t.Fatal("newGuardianApprover() did not preserve the Stack session service")
	}
}
