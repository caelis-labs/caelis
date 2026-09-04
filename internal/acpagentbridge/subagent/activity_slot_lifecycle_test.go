package subagent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func TestChildSlotRevokesPromptDispatchBeforeJoiningOperation(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-prompt-cancel"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	done := slot.beginPromptDispatch(cancelDispatch)
	_, revoke := slot.revokeActiveInput()
	if revoke == nil {
		t.Fatal("prompt dispatch did not publish its cancellation owner")
	}
	revoke()
	select {
	case <-dispatchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("prompt dispatch was not cancelled")
	}
	slot.finishPromptDispatch(done)
}

func TestBeginActivityKeepsPublishedRunSlotImmutable(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-slot-immutable"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			slot.beginActivity(fmt.Sprintf("activity-%d", i), run)
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			if got := run.childSlot(); got != slot {
				t.Errorf("childSlot() = %p, want %p", got, slot)
				return
			}
		}
	}()
	wg.Wait()
}
