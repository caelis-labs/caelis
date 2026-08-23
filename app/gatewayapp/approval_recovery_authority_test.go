package gatewayapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/hostownership"
)

func TestApprovalRecoveryGateUsesFileStoreCapabilityUnderLiveHostOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeDir := t.TempDir()
	priorAuthority, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = priorAuthority.Close() })

	priorStore, _ := sessionfile.NewStoreWithPriorHostFences(
		sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")},
		func(context.Context) (func(), bool) { return priorAuthority.Pin(storeDir) },
	)
	active, err := priorStore.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "host-owner", PreferredSessionID: "host-owner-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := priorStore.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "prior-host",
	}); err != nil {
		t.Fatal(err)
	}
	if err := priorAuthority.Close(); err != nil {
		t.Fatal(err)
	}

	currentAuthority, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = currentAuthority.Close() })
	store, capability := sessionfile.NewStoreWithPriorHostFences(
		sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")},
		func(context.Context) (func(), bool) { return currentAuthority.Pin(storeDir) },
	)
	replacer := approvalRecoveryFenceReplacer{fences: capability}
	gate := appserver.NewApprovalRecoveryGate(appserver.ApprovalRecoveryGateConfig{
		Store: store, FenceOwnerID: "current-host", PriorHostFences: replacer,
	})
	t.Cleanup(gate.Close)
	if err := gate.Wait(ctx); err != nil {
		t.Fatalf("startup recovery gate = %v", err)
	}
	if durable, err := store.SessionFence(ctx, active.SessionRef); err != nil || durable.FenceID != "" {
		t.Fatalf("fence after startup recovery = %#v, %v; want released", durable, err)
	}
	current, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "current-host",
	})
	if err != nil || current.OwnerID != "current-host" {
		t.Fatalf("ordinary admission after Host restart = %#v, %v", current, err)
	}
	if err := currentAuthority.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = replacer.ReplacePriorHostFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "next-host",
	})
	if !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("replacement after Host ownership close = %v, want ErrFenceConflict", err)
	}
	durable, err := store.SessionFence(ctx, active.SessionRef)
	if err != nil || durable.FenceID != current.FenceID || durable.OwnerID != current.OwnerID {
		t.Fatalf("fence after rejected replacement = %#v, %v; want %#v", durable, err, current)
	}
}
