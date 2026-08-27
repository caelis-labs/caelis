package taskstream

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

type DirectoryWatchRequest = controltaskstream.DirectoryWatchRequest
type DirectorySnapshot = controltaskstream.DirectorySnapshot
type DirectorySubscription = controltaskstream.DirectorySubscription
type DirectoryWatchResult = controltaskstream.DirectoryWatchResult

// DirectoryService is the optional realtime status capability implemented by
// Task services that can observe committed directory changes.
type DirectoryService interface {
	WatchDirectory(context.Context, Principal, DirectoryWatchRequest) (DirectoryWatchResult, error)
}

// DirectoryClient is the independently bound Surface-facing status capability.
type DirectoryClient interface {
	WatchDirectory(context.Context, DirectoryWatchRequest) (DirectoryWatchResult, error)
}

type serviceWithDirectory struct {
	*service
}

func (s *serviceWithDirectory) WatchDirectory(
	ctx context.Context,
	principal Principal,
	req DirectoryWatchRequest,
) (DirectoryWatchResult, error) {
	directory, ok := s.control.(controltaskstream.DirectoryService)
	if !ok {
		return DirectoryWatchResult{}, errorcode.New(errorcode.Unavailable, "taskstream: Task directory observation is unavailable")
	}
	result, err := directory.WatchDirectory(ctx, principal, req)
	if err != nil {
		return DirectoryWatchResult{}, err
	}
	if result.Subscription == nil {
		return DirectoryWatchResult{}, errorcode.New(errorcode.Unavailable, "taskstream: Control directory subscription is unavailable")
	}
	return result, nil
}

var _ Service = (*serviceWithDirectory)(nil)
var _ DirectoryService = (*serviceWithDirectory)(nil)
