package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestSpawnedChildRejectsV2BeforeSessionEffect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "session-called")
	registry, err := NewRegistry([]AgentConfig{{
		Name: "helper", Command: os.Args[0], Args: []string{"-test.run=TestRunnerSteeringCapabilityHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_STEERING_HELPER": "1", "CAELIS_ACP_STEERING_META": `{"supported":true}`,
			"CAELIS_ACP_SESSION_EFFECT_MARKER": marker, "CAELIS_ACP_TEST_PROTOCOL_VERSION": "2",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID: "task-incompatible", ActivityID: "activity-incompatible", CWD: t.TempDir(),
		Output: &recordingStreams{},
	}, delegation.Request{Agent: "helper", Prompt: "must not execute"})
	if err == nil || !strings.Contains(err.Error(), "requires stable v1") || anchor.SessionID != "" {
		t.Fatalf("spawn incompatible peer = %#v, %v", anchor, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Session effect reached incompatible peer: %v", err)
	}
	runner.mu.RLock()
	registered := len(runner.runs)
	runner.mu.RUnlock()
	if registered != 0 {
		t.Fatalf("incompatible peer registered %d child runs", registered)
	}
}
