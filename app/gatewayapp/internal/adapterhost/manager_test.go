package adapterhost

import (
	"context"
	"io"
	"path/filepath"
	"strings"
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

type recordingProtocolBackend struct {
	unusedBackend
	stableCalls int
	draftCalls  int
	schema      string
	channel     controladapterhost.ChannelContext
}

func (b *recordingProtocolBackend) ServeACP(_ context.Context, channel controladapterhost.ChannelContext, _ io.Reader, _ io.Writer) error {
	b.stableCalls++
	b.channel = channel
	return nil
}

func (b *recordingProtocolBackend) ServeACPDraft(_ context.Context, channel controladapterhost.ChannelContext, schema string, _ io.Reader, _ io.Writer) error {
	b.draftCalls++
	b.schema, b.channel = schema, channel
	return nil
}

func TestDraftChannelUsesOnlyGrantedSchemaAndOriginalWorkspace(t *testing.T) {
	const schema = "schema-v2.0.0-alpha.8"
	manager := newGrantTestManager(t)
	manager.mu.Lock()
	registration := manager.registrations["test"]
	registration.ACPDraftSchemas = []string{schema}
	manager.registrations["test"] = registration
	backend := &recordingProtocolBackend{}
	manager.running["test"] = &runningBackend{backend: backend}
	manager.mu.Unlock()
	// This fixture injects a backend with no OS process; leave the existing
	// supervisor cleanup to its process-owning tests.
	t.Cleanup(func() {
		manager.mu.Lock()
		delete(manager.running, "test")
		manager.mu.Unlock()
	})
	root := t.TempDir()
	request := controladapterhost.GrantRequest{ConnectionID: "connection", CWD: root, WritableRoots: []string{root}, ACPDraftSchema: "schema-v2.0.0-alpha.7"}
	if _, err := manager.IssueGrant(t.Context(), "principal", "test", request); err == nil {
		t.Fatal("unsupported draft was granted")
	}
	request.ACPDraftSchema = schema
	grant, err := manager.IssueGrant(t.Context(), "principal", "test", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ServeChannel(t.Context(), "test", grant.Token, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if backend.stableCalls != 0 || backend.draftCalls != 1 || backend.schema != schema || backend.channel.ACPDraftSchema != schema {
		t.Fatalf("draft dispatch = %#v", backend)
	}
	if backend.channel.PrincipalID != "principal" || backend.channel.ConnectionID != "connection" || !equalStrings(backend.channel.AllowedRoots, []string{root}) || !equalStrings(backend.channel.WritableRoots, []string{root}) {
		t.Fatalf("draft changed authority = %#v", backend.channel)
	}
	if err := manager.ServeChannel(t.Context(), "test", grant.Token, nil, nil); err == nil {
		t.Fatal("draft grant reused")
	}
	request.ACPDraftSchema = ""
	stable, err := manager.IssueGrant(t.Context(), "principal", "test", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ServeChannel(t.Context(), "test", stable.Token, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if backend.stableCalls != 1 || backend.draftCalls != 1 || backend.channel.ACPDraftSchema != "" {
		t.Fatalf("stable dispatch = %#v", backend)
	}
}

func TestDraftChannelNeverFallsBackToStableBackend(t *testing.T) {
	manager := newGrantTestManager(t)
	manager.mu.Lock()
	registration := manager.registrations["test"]
	registration.ACPDraftSchemas = []string{"fixture-draft"}
	manager.registrations["test"] = registration
	manager.running["test"] = &runningBackend{backend: unusedBackend{}}
	manager.mu.Unlock()
	t.Cleanup(func() {
		manager.mu.Lock()
		delete(manager.running, "test")
		manager.mu.Unlock()
	})
	grant, err := manager.IssueGrant(t.Context(), "principal", "test", controladapterhost.GrantRequest{ConnectionID: "connection", CWD: t.TempDir(), ACPDraftSchema: "fixture-draft"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ServeChannel(t.Context(), "test", grant.Token, nil, nil); err == nil || !strings.Contains(err.Error(), "draft backend is unavailable") {
		t.Fatalf("draft fell back or wrong rejection: %v", err)
	}
}
