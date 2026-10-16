//go:build windows

package windows

import (
	"context"
	"os"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestRuntimeCloseDrainsRefreshBeforeReleasingState(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	r := rt.(*runtime)
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctx, started := r.beginRefresh(parent)
		if !started {
			t.Fatal("refresh was not admitted")
		}
		releaseUse, err := r.beginRuntimeUse()
		if err != nil {
			r.finishRefresh(err)
			t.Fatal(err)
		}
		finish := make(chan struct{})
		finishUse := sync.OnceFunc(func() { close(finish) })
		defer finishUse()
		go func() {
			<-ctx.Done()
			<-finish
			releaseUse()
			r.finishRefresh(ctx.Err())
		}()
		closed := make(chan error, 1)
		go func() { closed <- rt.Close() }()
		synctest.Wait()
		if ctx.Err() != context.Canceled {
			t.Errorf("Close did not cancel refresh: %v", ctx.Err())
		}
		returned := false
		select {
		case err := <-closed:
			returned = true
			t.Errorf("Close returned before refresh released its use: %v", err)
		default:
		}
		finishUse()
		if !returned {
			if err := <-closed; err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}
	})
	if running, _, _, _, _ := r.refreshSnapshot(); running {
		t.Fatal("refresh is still active after Close")
	}
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatalf("remove state immediately after Close: %v", err)
	}
	r.startBackgroundRefresh(context.Background(), sandbox.CommandRequest{Dir: r.cfg.CWD})
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("refresh after Close recreated state: %v", err)
	}
}
