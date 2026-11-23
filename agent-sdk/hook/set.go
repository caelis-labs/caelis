package hook

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Validate checks registration and stream combinations before any input commits.
func (h Set) Validate() error {
	if err := validateEntries("Input", h.Input); err != nil {
		return err
	}
	if err := validateEntries("TurnStart", h.TurnStart); err != nil {
		return err
	}
	if err := validateEntries("ModelPrepare", h.ModelPrepare); err != nil {
		return err
	}
	if err := validateEntries("ModelInvoke", h.ModelInvoke); err != nil {
		return err
	}
	if err := validateEntries("ModelResult", h.ModelResult); err != nil {
		return err
	}
	if err := validateEntries("ToolPrepare", h.ToolPrepare); err != nil {
		return err
	}
	if err := validateEntries("ToolInvoke", h.ToolInvoke); err != nil {
		return err
	}
	if err := validateEntries("ToolResult", h.ToolResult); err != nil {
		return err
	}
	if err := validateEntries("ModelView", h.ModelView); err != nil {
		return err
	}
	if err := validateEntries("Completion", h.Completion); err != nil {
		return err
	}
	if err := validateEntries("Stream", h.Stream); err != nil {
		return err
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

// Clone fixes registration containers while retaining function identity.
func (h Set) Clone() Set {
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
func (h Set) HasLoopPhases() bool {
	return h.StreamMode == StreamBuffered || len(h.ModelPrepare)+len(h.ModelInvoke)+len(h.ModelResult)+len(h.ToolPrepare)+len(h.ToolInvoke)+len(h.ToolResult)+len(h.ModelView)+len(h.Completion)+len(h.Stream) > 0
}

func validateEntries[F any](phase string, entries []BaseHook[F]) error {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name
		if name == "" || name != strings.TrimSpace(name) || seen[name] {
			return fmt.Errorf("hooks %s: empty, untrimmed or duplicate name %q", phase, name)
		}
		seen[name] = true
		if entry.Timeout < 0 {
			return fmt.Errorf("hooks %s/%s: negative timeout", phase, name)
		}
		if reflect.ValueOf(entry.Apply).IsNil() {
			return fmt.Errorf("hooks %s/%s: nil function", phase, name)
		}
	}
	return nil
}
