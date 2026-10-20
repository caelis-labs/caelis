package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	// RetireToolName marks session exposure for retirement at a durable checkpoint.
	RetireToolName = "tool_retire"
	// MetadataToolExposure carries the versioned session exposure projection.
	MetadataToolExposure = "caelis.tool_exposure.v1"
	retirementKind       = "tool_retirement"
)

// Binding identifies a canonical remote tool independently of its projected
// name. Version binds configuration, implementation and schema, using the MCP
// source fingerprint; a same-name replacement cannot inherit exposure.
type Binding struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Version  string `json:"version"`
}

// BindingForDefinition returns an opaque source identity and definition version.
// Incomplete external definitions cannot participate in experimental retirement.
func BindingForDefinition(def Definition) Binding {
	field := func(k string) string { value, _ := def.Metadata[k].(string); return value }
	remote, server, version := field(MetadataMCPTool), field(MetadataMCPServer), field(MetadataMCPSourceFingerprint)
	if !IsMCPDefinition(def) || remote == "" || server == "" || version == "" {
		return Binding{}
	}
	raw, _ := json.Marshal([]string{field(MetadataPluginID), server, remote})
	sum := sha256.Sum256(raw)
	return Binding{Name: def.Name, Identity: hex.EncodeToString(sum[:]), Version: version}
}

// ExposureState is a durable projection, never execution authority. Pending
// marks leave Active unchanged until Runtime commits a compaction checkpoint.
type ExposureState struct {
	Version int       `json:"version"`
	Active  []Binding `json:"active"`
	Pending []Binding `json:"pending,omitempty"`
}

// ExposureFromMetadata reads a detached state. An unknown version is an empty
// exposure, requiring rediscovery rather than restoring tools by name.
func ExposureFromMetadata(meta map[string]any) (ExposureState, bool) {
	value, ok := meta[MetadataToolExposure]
	if !ok {
		return ExposureState{}, false
	}
	raw, err := json.Marshal(value)
	var state ExposureState
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Version != 1 {
		return ExposureState{Version: 1}, true
	}
	return state, true
}

// NewRetirementTool returns the experimental marking tool. ToolVisibility hides
// it until at least one deferred tool is exposed and validates exact bindings.
// Runtime must wrap it with normal policy and execution journaling.
func NewRetirementTool() Tool {
	binding := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"name", "identity", "version"}, "properties": map[string]any{
		"name": map[string]any{"type": "string"}, "identity": map[string]any{"type": "string"}, "version": map[string]any{"type": "string"},
	}}
	return NamedTool{Def: Definition{Name: RetireToolName, Description: "Mark discovered tools no longer needed, or cancel marks, using exact name, identity and version from tool_search. Marks only reduce this session's exposed tools after successful compaction. History, permissions, servers and plugins are unchanged. Searching or using a marked tool cancels its mark.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"action", "tools"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []any{"mark", "cancel"}},
			"tools":  map[string]any{"type": "array", "minItems": 1, "maxItems": MaxDeferredToolsPerRun, "items": binding},
		},
	}, Metadata: map[string]any{MetadataToolKind: retirementKind}}, Invoke: func(_ context.Context, call Call) (Result, error) {
		var request retirementRequest
		decoder := json.NewDecoder(bytes.NewReader(call.Input))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return Result{}, err
		}
		if (request.Action != "mark" && request.Action != "cancel") || len(request.Tools) == 0 || len(request.Tools) > MaxDeferredToolsPerRun {
			return Result{}, errors.New("invalid tool retirement request")
		}
		raw, _ := json.Marshal(request)
		return Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(raw)}}, nil
	}}
}

type retirementRequest struct {
	Action string    `json:"action"`
	Tools  []Binding `json:"tools"`
}

// RetirementEnabled reports whether this visibility has the opt-in manager.
func (v *ToolVisibility) RetirementEnabled() bool { return v != nil && v.retirement }

// RestoreExposure installs a checkpoint projection at a safe request boundary.
// Only exact current bindings become visible. Unknown/replaced tools require
// discovery; historical tool results are not changed.
func (v *ToolVisibility) RestoreExposure(state ExposureState) {
	if !v.RetirementEnabled() {
		return
	}
	for name := range v.deferred {
		delete(v.visible, name)
	}
	v.deferredCount = 0
	v.deferredPromptTokens = 0
	v.exposure = ExposureState{Version: 1, Active: slices.Clone(state.Active), Pending: slices.Clone(state.Pending)}
	for _, binding := range state.Active {
		if BindingForDefinition(v.definitions[binding.Name]) == binding && binding.Identity != "" {
			v.Reveal(binding.Name)
		}
	}
}

// Exposure returns durable bindings, including those not yet resolved by a
// ready catalog. Live availability never erases persisted discovery facts.
func (v *ToolVisibility) Exposure() ExposureState {
	state := ExposureState{Version: 1}
	if !v.RetirementEnabled() {
		return state
	}
	state.Active = slices.Clone(v.exposure.Active)
	for _, item := range v.tools {
		name := item.Definition().Name
		if v.deferred[name] && v.visible[name] {
			binding := BindingForDefinition(v.definitions[name])
			if binding.Identity != "" && !slices.Contains(state.Active, binding) {
				state.Active = slices.DeleteFunc(state.Active, func(old Binding) bool { return old.Name == binding.Name })
				state.Active = append(state.Active, binding)
			}
		}
	}
	for _, binding := range v.exposure.Pending {
		if slices.Contains(state.Active, binding) {
			state.Pending = append(state.Pending, binding)
		}
	}
	return state
}

// AdmitRetirement validates the whole mark/cancel request without hiding tools.
func (v *ToolVisibility) AdmitRetirement(output map[string]any) error {
	if !v.RetirementEnabled() || v.deferredCount == 0 {
		return errors.New("tool retirement is unavailable")
	}
	raw, _ := json.Marshal(output)
	var request retirementRequest
	if json.Unmarshal(raw, &request) != nil || (request.Action != "mark" && request.Action != "cancel") || len(request.Tools) == 0 {
		return errors.New("invalid tool retirement request")
	}
	state := v.Exposure()
	for _, binding := range request.Tools {
		if !slices.Contains(state.Active, binding) {
			return errors.New("tool retirement binding is stale or not exposed")
		}
	}
	for _, binding := range request.Tools {
		state.Pending = slices.DeleteFunc(state.Pending, func(item Binding) bool { return item == binding })
		if request.Action == "mark" {
			state.Pending = append(state.Pending, binding)
		}
	}
	v.exposure = state
	return nil
}

// KeepTool cancels a pending mark when a tool is rediscovered or used.
func (v *ToolVisibility) KeepTool(name string) {
	if !v.RetirementEnabled() {
		return
	}
	v.exposure.Pending = slices.DeleteFunc(v.exposure.Pending, func(binding Binding) bool { return binding.Name == name })
}
