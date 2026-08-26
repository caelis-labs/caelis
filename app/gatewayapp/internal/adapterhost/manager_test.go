package adapterhost

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
)

type unusedBackend struct{}

func (unusedBackend) ServeACP(context.Context, controladapterhost.ChannelContext, io.Reader, io.Writer) error {
	return nil
}
func (unusedBackend) Done() <-chan struct{} { return make(chan struct{}) }
func (unusedBackend) Err() error            { return nil }
func (unusedBackend) Close() error          { return nil }

func newGrantTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewManager(Registration{
		ID: "test", Name: "Test", Command: "unused",
		NewBackend: func(context.Context, io.Reader, io.Writer) (Backend, error) { return unusedBackend{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestIssueGrantNormalizesWorkspaceAuthority(t *testing.T) {
	manager := newGrantTestManager(t)
	root := t.TempDir()
	child := filepath.Join(root, "child")
	grant, err := manager.IssueGrant(context.Background(), "principal", "test", controladapterhost.GrantRequest{
		ConnectionID: "connection", CWD: child,
		AllowedRoots: []string{root, child, root}, WritableRoots: []string{child, child},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	stored := manager.grants[grant.Token]
	manager.mu.Unlock()
	if stored.context.PrincipalID != "principal" || stored.context.ConnectionID != "connection" {
		t.Fatalf("grant identity = %#v", stored.context)
	}
	if got, want := stored.context.AllowedRoots, []string{child, root}; !equalStrings(got, want) {
		t.Fatalf("allowed roots = %v, want %v", got, want)
	}
	if got, want := stored.context.WritableRoots, []string{child}; !equalStrings(got, want) {
		t.Fatalf("writable roots = %v, want %v", got, want)
	}
}

func TestServeChannelConsumesGrantBeforeRouteValidation(t *testing.T) {
	manager := newGrantTestManager(t)
	root := t.TempDir()
	grant, err := manager.IssueGrant(context.Background(), "principal", "test", controladapterhost.GrantRequest{
		ConnectionID: "connection", CWD: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ServeChannel(context.Background(), "other", grant.Token, nil, nil); err == nil {
		t.Fatal("ServeChannel() with wrong adapter succeeded")
	}
	if err := manager.ServeChannel(context.Background(), "test", grant.Token, nil, nil); err == nil {
		t.Fatal("ServeChannel() reused an already-consumed grant")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
