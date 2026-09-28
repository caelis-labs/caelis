package session

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestEqualExecutionConfigTreatsEmptyCollectionsAsNoOverrides(t *testing.T) {
	empty := &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
		Set: map[string]string{}, Unset: []string{},
	}}
	if !EqualExecutionConfig(empty, &sandbox.ExecutionConfig{}) {
		t.Fatal("empty collection overrides differ after durable serialization")
	}
	if EqualExecutionConfig(empty, &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
		Set: map[string]string{"KEY": "value"},
	}}) {
		t.Fatal("changed creation-bound environment matched")
	}
}
