package taskstream

import (
	"context"
	"errors"
	"strings"
)

// Client is the principal-bound Task observation contract consumed by one
// presentation client. Task lifecycle mutations remain on the Task control
// plane; closing a Subscription only detaches this observer.
type Client interface {
	List(context.Context, ListRequest) (ListResult, error)
	Events(context.Context, ReadRequest) (ReadResult, error)
	Subscribe(context.Context, SubscribeRequest) (SubscribeResult, error)
}

type boundClient struct {
	service   Service
	principal Principal
}

// BindClient binds one trusted principal to the in-process Task observation
// service. Network adapters implement the same Client contract after
// authentication has bound their principal at the Host.
func BindClient(service Service, principal Principal) (Client, error) {
	if service == nil {
		return nil, errors.New("taskstream: service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("taskstream: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	client := &boundClient{service: service, principal: principal}
	if _, ok := service.(DirectoryService); ok {
		return &boundDirectoryClient{boundClient: client}, nil
	}
	return client, nil
}

func (c *boundClient) List(ctx context.Context, request ListRequest) (ListResult, error) {
	return c.service.List(ctx, c.boundPrincipal(), request)
}

func (c *boundClient) Events(ctx context.Context, request ReadRequest) (ReadResult, error) {
	return c.service.Events(ctx, c.boundPrincipal(), request)
}

func (c *boundClient) Subscribe(ctx context.Context, request SubscribeRequest) (SubscribeResult, error) {
	return c.service.Subscribe(ctx, c.boundPrincipal(), request)
}

func (c *boundClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

type boundDirectoryClient struct {
	*boundClient
}

func (c *boundDirectoryClient) WatchDirectory(ctx context.Context, request DirectoryWatchRequest) (DirectoryWatchResult, error) {
	service, ok := c.service.(DirectoryService)
	if !ok {
		return DirectoryWatchResult{}, errors.New("taskstream: Task directory observation is unavailable")
	}
	return service.WatchDirectory(ctx, c.boundPrincipal(), request)
}

var _ Client = (*boundClient)(nil)
var _ Client = (*boundDirectoryClient)(nil)
var _ DirectoryClient = (*boundDirectoryClient)(nil)
