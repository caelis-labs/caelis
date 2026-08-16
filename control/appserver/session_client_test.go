package appserver

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestBindSessionClientMatchesPrincipalBoundClientContract(t *testing.T) {
	service := &boundClientService{}
	principal := Principal{ID: " owner ", Roles: []string{"operator"}}
	client, err := BindSessionClient(service, principal)
	if err != nil {
		t.Fatal(err)
	}
	principal.Roles[0] = "mutated"

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion != schema.CurrentProtocolVersion ||
		info.EnvelopeVersion != EnvelopeVersion ||
		info.APIVersion != HTTPAPIVersion {
		t.Fatalf("Initialize() = %#v", info)
	}

	result, err := client.CreateSession(context.Background(), CreateSessionRequest{
		WriteBase:          WriteBase{OperationID: "operation-1"},
		PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "session-1" ||
		service.principal.ID != "owner" ||
		len(service.principal.Roles) != 1 ||
		service.principal.Roles[0] != "operator" {
		t.Fatalf("CreateSession result/principal = %#v / %#v", result, service.principal)
	}
}

func TestBindSessionClientRejectsMissingAuthority(t *testing.T) {
	if _, err := BindSessionClient(nil, Principal{ID: "owner"}); err == nil {
		t.Fatal("BindSessionClient accepted a nil Service")
	}
	if _, err := BindSessionClient(&boundClientService{}, Principal{}); err == nil {
		t.Fatal("BindSessionClient accepted an empty principal")
	}
}

type boundClientService struct {
	Service
	principal Principal
}

func (s *boundClientService) CreateSession(_ context.Context, principal Principal, request CreateSessionRequest) (CommandResult, error) {
	s.principal = principal
	return CommandResult{
		OperationID: request.OperationID,
		Outcome:     OutcomeCommitted,
		SessionID:   request.PreferredSessionID,
	}, nil
}

func (s *boundClientService) ListSessions(context.Context, Principal, ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}
