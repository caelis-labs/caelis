package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/plugin"
)

func TestPluginHostKeepsMutationSerializedThroughAfterCommit(t *testing.T) {
	storeDir := t.TempDir()
	stack := &Stack{store: newAppConfigStore(storeDir), storeDir: storeDir}
	host := pluginHost{stack: stack}

	afterCommitEntered := make(chan struct{})
	releaseAfterCommit := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseAfterCommit) })

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- host.UpdatePluginState(context.Background(), plugin.Mutation{
			GuardAction: "first plugin mutation",
			Apply:       func(*plugin.State) error { return nil },
			AfterCommit: func(plugin.State) error {
				close(afterCommitEntered)
				<-releaseAfterCommit
				return nil
			},
		})
	}()

	select {
	case <-afterCommitEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first mutation did not reach AfterCommit")
	}

	secondApplyEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- host.UpdatePluginState(context.Background(), plugin.Mutation{
			GuardAction: "second plugin mutation",
			Apply: func(*plugin.State) error {
				close(secondApplyEntered)
				return nil
			},
		})
	}()

	select {
	case <-secondApplyEntered:
		t.Fatal("second mutation entered before the first AfterCommit completed")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseAfterCommit) })
	if err := <-firstDone; err != nil {
		t.Fatalf("first UpdatePluginState() error = %v", err)
	}
	select {
	case <-secondApplyEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second mutation remained blocked after AfterCommit completed")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second UpdatePluginState() error = %v", err)
	}
}

func TestPluginHostAfterCommitErrorDoesNotRollbackCommittedState(t *testing.T) {
	storeDir := t.TempDir()
	stack := &Stack{store: newAppConfigStore(storeDir), storeDir: storeDir}
	host := pluginHost{stack: stack}
	sentinel := errors.New("cleanup failed")

	err := host.UpdatePluginState(context.Background(), plugin.Mutation{
		GuardAction: "remove plugin",
		Apply: func(state *plugin.State) error {
			state.Plugins = []plugin.Config{{ID: "committed"}}
			return nil
		},
		AfterCommit: func(plugin.State) error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("UpdatePluginState() error = %v, want AfterCommit error", err)
	}

	state, loadErr := host.LoadPluginState(context.Background())
	if loadErr != nil {
		t.Fatalf("LoadPluginState() error = %v", loadErr)
	}
	if len(state.Plugins) != 1 || state.Plugins[0].ID != "committed" {
		t.Fatalf("committed state = %#v, want plugin retained", state)
	}
}
