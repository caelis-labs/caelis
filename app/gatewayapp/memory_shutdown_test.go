package gatewayapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
)

func TestStackCloseRetainsMemoryRuntimeUntilStewardDrains(t *testing.T) {
	runtime, err := memoryhost.Open(t.Context(), memoryhost.Config{
		DataDir:     t.TempDir(),
		Credentials: func(context.Context, string) (string, error) { return "unused", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	stack := &Stack{
		memoryRuntime: runtime,
		memorySteward: &memoryStewardBridge{done: done},
	}
	stack.composition.authorities.memoryHost = runtime

	err = stack.closeWithQuiesceTimeout(time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "close deferred until Memory Steward drains") {
		t.Fatalf("first Close() error = %v", err)
	}
	if stack.memoryRuntime != runtime || runtime.Management() == nil {
		t.Fatal("Close() released Memory runtime before Steward drained")
	}

	close(done)
	if err := stack.closeWithQuiesceTimeout(time.Second); err != nil {
		t.Fatalf("retry Close() = %v", err)
	}
	if stack.memoryRuntime != nil || runtime.Management() != nil {
		t.Fatal("retry Close() did not release drained Memory runtime")
	}
}
