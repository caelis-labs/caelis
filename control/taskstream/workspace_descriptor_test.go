package taskstream

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestTaskDescriptorKeepsChildModelAndLosslessContextGauge(t *testing.T) {
	entry := &task.Entry{Kind: task.KindSubagent, Spec: map[string]any{"target": map[string]any{"placement": map[string]any{"model": "assigned-model"}}}}
	if got := descriptorFromEntry(entry).Model; got != "assigned-model" {
		t.Fatalf("model=%q", got)
	}
	entry.ContextUsage = &task.ContextUsageRecord{Snapshot: session.ContextUsageSnapshot{Used: math.MaxUint64 - 1, Size: math.MaxUint64}, Invocation: session.EventInvocation{Model: "observed-model"}}
	descriptor := descriptorFromEntry(entry)
	if descriptor.Model != "observed-model" {
		t.Fatalf("usage model=%q", descriptor.Model)
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["context_size"] != strconv.FormatUint(math.MaxUint64, 10) {
		t.Fatalf("context lost precision=%s", raw)
	}
	var restored TaskDescriptor
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ContextSize != descriptor.ContextSize || restored.ContextUsed != descriptor.ContextUsed {
		t.Fatalf("gauge roundtrip=%#v", restored)
	}
}
