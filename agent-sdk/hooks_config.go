package agentsdk

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Validate checks registration and stream combinations before any input commits.
func (h Hooks) Validate() error {
	value := reflect.ValueOf(h)
	for i := 0; i < value.NumField(); i++ {
		entries := value.Field(i)
		if entries.Kind() != reflect.Slice {
			continue
		}
		phase := value.Type().Field(i).Name
		seen := map[string]bool{}
		for j := 0; j < entries.Len(); j++ {
			entry := entries.Index(j)
			name := entry.FieldByName("Name").String()
			if name == "" || name != strings.TrimSpace(name) || seen[name] {
				return fmt.Errorf("hooks %s: empty, untrimmed or duplicate name %q", phase, name)
			}
			seen[name] = true
			if entry.FieldByName("Timeout").Interface().(time.Duration) < 0 {
				return fmt.Errorf("hooks %s/%s: negative timeout", phase, name)
			}
			fn := entry.FieldByName("Apply")
			if !fn.IsValid() {
				fn = entry.FieldByName("Invoke")
			}
			if fn.IsNil() {
				return fmt.Errorf("hooks %s/%s: nil function", phase, name)
			}
		}
	}
	if h.CompletionLimit < 0 {
		return fmt.Errorf("hooks: negative completion limit")
	}
	switch h.StreamMode {
	case "", StreamBuffered:
	case StreamIncremental:
		if len(h.ModelResult) > 0 || len(h.Completion) > 0 {
			return fmt.Errorf("hooks: incremental output cannot combine with final response/completion transformations")
		}
	default:
		return fmt.Errorf("hooks: invalid stream mode %q", h.StreamMode)
	}
	return nil
}

// CloneHooks fixes registration containers while retaining function identity.
func CloneHooks(h Hooks) Hooks {
	h.Input = slices.Clone(h.Input)
	h.TurnStart = slices.Clone(h.TurnStart)
	h.ModelPrepare = slices.Clone(h.ModelPrepare)
	h.ModelInvoke = slices.Clone(h.ModelInvoke)
	h.ModelResult = slices.Clone(h.ModelResult)
	h.ToolPrepare = slices.Clone(h.ToolPrepare)
	h.ToolInvoke = slices.Clone(h.ToolInvoke)
	h.ToolResult = slices.Clone(h.ToolResult)
	h.ModelView = slices.Clone(h.ModelView)
	h.Completion = slices.Clone(h.Completion)
	h.Stream = slices.Clone(h.Stream)
	return h
}

// HasLoopPhases reports requirements unavailable inside an external Agent's
// private loop. Embeddings must reject them instead of claiming protection.
func (h Hooks) HasLoopPhases() bool {
	return h.StreamMode == StreamBuffered || len(h.ModelPrepare)+len(h.ModelInvoke)+len(h.ModelResult)+len(h.ToolPrepare)+len(h.ToolInvoke)+len(h.ToolResult)+len(h.ModelView)+len(h.Completion)+len(h.Stream) > 0
}
