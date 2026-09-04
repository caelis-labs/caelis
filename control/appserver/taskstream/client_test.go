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

func TestBindClientAdvertisesDirectoryOnlyWhenServiceImplementsIt(t *testing.T) {
	t.Parallel()

	base, err := BindClient(&clientTestService{}, Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base.(DirectoryClient); ok {
		t.Fatal("base Task client unexpectedly advertised directory observation")
	}

	service := &clientTestDirectoryService{clientTestService: &clientTestService{}}
	bound, err := BindClient(service, Principal{ID: "owner", Roles: []string{"observer"}})
	if err != nil {
		t.Fatal(err)
	}
	directory, ok := bound.(DirectoryClient)
	if !ok {
		t.Fatal("directory-capable Task service lost its optional client capability")
	}
	if _, err := directory.WatchDirectory(context.Background(), DirectoryWatchRequest{SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	if service.principal.ID != "owner" || service.sessionID != "session-1" {
		t.Fatalf("directory binding = principal %#v session %q", service.principal, service.sessionID)
	}
}

type clientTestService struct {
	principal Principal
}

func (s *clientTestService) List(_ context.Context, principal Principal, _ ListRequest) (ListResult, error) {
	s.principal = principal
	return ListResult{}, nil
}

func (*clientTestService) Events(context.Context, Principal, ReadRequest) (ReadResult, error) {
	return ReadResult{}, nil
}

func (*clientTestService) Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error) {
	return SubscribeResult{}, nil
}

var _ Service = (*clientTestService)(nil)

type clientTestDirectoryService struct {
	*clientTestService
	sessionID string
}

func (s *clientTestDirectoryService) WatchDirectory(
	_ context.Context,
	principal Principal,
	request DirectoryWatchRequest,
) (DirectoryWatchResult, error) {
	s.principal = principal
	s.sessionID = request.SessionID
	return DirectoryWatchResult{Subscription: clientTestDirectorySubscription{}}, nil
}

type clientTestDirectorySubscription struct{}

func (clientTestDirectorySubscription) Snapshots() <-chan DirectorySnapshot {
	closed := make(chan DirectorySnapshot)
	close(closed)
	return closed
}

func (clientTestDirectorySubscription) Close() error { return nil }
func (clientTestDirectorySubscription) Err() error   { return nil }

var _ DirectoryService = (*clientTestDirectoryService)(nil)
var _ DirectorySubscription = clientTestDirectorySubscription{}
