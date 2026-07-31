package taskstream

import (
	"context"
	"testing"
)

func TestBindClientPinsPrincipal(t *testing.T) {
	t.Parallel()

	service := &clientTestService{}
	client, err := BindClient(service, Principal{ID: " owner ", Roles: []string{"observer"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(context.Background(), ListRequest{SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	if service.principal.ID != "owner" ||
		len(service.principal.Roles) != 1 ||
		service.principal.Roles[0] != "observer" {
		t.Fatalf("principal = %#v", service.principal)
	}
}

type clientTestService struct {
	principal Principal
}

func (s *clientTestService) List(_ context.Context, principal Principal, _ ListRequest) (ListResult, error) {
	s.principal = principal
	return ListResult{}, nil
}

func (*clientTestService) Events(context.Context, Principal, ReadRequest) (Batch, error) {
	return Batch{}, nil
}

func (*clientTestService) Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error) {
	return SubscribeResult{}, nil
}

var _ Service = (*clientTestService)(nil)
