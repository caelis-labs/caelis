package runtime

import (
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestCommandHandleLookupDuringApprovalRefresh(t *testing.T) {
	ref := session.SessionRef{SessionID: "identity-session"}
	command := &commandTask{sessionRef: ref, handle: "command", state: taskapi.StateWaitingApproval, running: true}
	tasks := &taskRuntime{tasks: map[string]*commandTask{"command-id": command}}
	entry := &taskapi.Entry{TaskID: "command-id", Session: ref, Handle: "command", State: taskapi.StateWaitingApproval, Running: true}
	var group sync.WaitGroup
	group.Go(func() {
		for range 2000 {
			applyCommandEntry(command, entry)
		}
	})
	group.Go(func() {
		for range 2000 {
			identity, err := tasks.resolveTaskHandle(t.Context(), ref, "command")
			if err != nil || identity.taskID != "command-id" {
				t.Errorf("wrong original identity %+v %v", identity, err)
				return
			}
		}
	})
	group.Wait()
}
