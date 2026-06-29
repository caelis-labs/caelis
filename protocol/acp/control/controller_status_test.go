package control

import (
	"context"
	"errors"
	"testing"
)

func TestActiveACPStatus(t *testing.T) {
	if status, active := ActiveACPStatus(context.Background(), nil); active || status.ControllerKind != "" {
		t.Fatalf("ActiveACPStatus(nil) = %#v %v, want inactive empty status", status, active)
	}
	if _, active := ActiveACPStatus(context.Background(), testAgentStatusProvider{err: errors.New("boom")}); active {
		t.Fatal("ActiveACPStatus(error) active = true, want false")
	}
	if _, active := ActiveACPStatus(context.Background(), testAgentStatusProvider{status: AgentStatusSnapshot{ControllerKind: "kernel"}}); active {
		t.Fatal("ActiveACPStatus(kernel) active = true, want false")
	}
	status, active := ActiveACPStatus(context.Background(), testAgentStatusProvider{status: AgentStatusSnapshot{ControllerKind: " ACP "}})
	if !active || status.ControllerKind != " ACP " {
		t.Fatalf("ActiveACPStatus(acp) = %#v %v, want active original status", status, active)
	}
}

type testAgentStatusProvider struct {
	status AgentStatusSnapshot
	err    error
}

func (p testAgentStatusProvider) AgentStatus(context.Context) (AgentStatusSnapshot, error) {
	return p.status, p.err
}
