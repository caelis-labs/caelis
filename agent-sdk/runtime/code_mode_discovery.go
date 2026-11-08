package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeSearchKey struct{}

// Nested search uses canonical admission before the existing Journal records
// full schemas/bindings. Ordinary native ToolSearch keeps its Chat owner.
type codeModeDiscoveryTool struct {
	base   tool.Tool
	lookup func(context.Context, string) (tool.Tool, bool, error)
}

func (t codeModeDiscoveryTool) Definition() tool.Definition { return t.base.Definition() }
func (t codeModeDiscoveryTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	result, err := t.base.Call(ctx, call)
	if err != nil || result.IsError {
		return result, err
	}
	if nested, _ := ctx.Value(codeModeSearchKey{}).(bool); !nested {
		return result, nil
	}
	var selected tool.ToolSearchResult
	for _, p := range result.Content {
		if p.JSON != nil {
			var raw map[string]any
			if json.Unmarshal(p.JSON.Value, &raw) == nil {
				selected = tool.ParseToolSearchOutput(raw)
			}
			break
		}
	}
	current := map[string]tool.Tool{}
	for _, entry := range selected.Tools {
		one, found, lookupErr := t.lookup(ctx, entry.Name)
		if lookupErr != nil {
			return result, lookupErr
		}
		if found && one != nil {
			def := one.Definition()
			if tool.IsMCPDefinition(def) && def.AvailableForModel(call.RuntimeModel) && codeModeCanCall(def) {
				current[def.Name] = one
			}
		}
	}
	return mcpdiscovery.Admit(selected, current, result)
}

func codeModeCanCall(def tool.Definition) bool {
	// Aliases preserve this real infrastructure requirement. Domain exec is not
	// native CommandExec merely because its display name contains "exec".
	return def.ExecutionRequirements == nil || !def.ExecutionRequirements.Sandbox.CommandExec
}

type codeModeSearchEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (h *codeModeHost) Search(ctx context.Context, query string) (reply codemode.Reply, discoveryErr error) {
	h.mu.Lock()
	before := len(h.failures)
	closed := h.closed || h.ctx.Err() != nil
	h.mu.Unlock()
	defer func() {
		if discoveryErr != nil {
			h.mu.Lock()
			h.isError = true
			if len(h.failures) == before {
				h.failures = append(h.failures, map[string]string{"tool": "search", "outcome": codemode.OutcomeFailed, "code": string(errorcode.CodeOf(discoveryErr))})
			}
			h.mu.Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return reply, err
	}
	if closed {
		return reply, tool.NewError(errorcode.FailedPrecondition, "code mode host admission is closed")
	}
	if len(query) == 0 || len(query) > 1024 {
		return reply, tool.NewError(tool.ErrorCodeInvalidInput, "invalid search query")
	}
	one, found, err := h.owner.lookup(ctx, query)
	if err != nil {
		return reply, err
	}
	exact := false
	if found && one != nil {
		def := one.Definition()
		exact = def.Name == query && tool.IsMCPDefinition(def)
		if def.Name == query && !tool.IsMCPDefinition(def) && !tool.IsToolSearchDefinition(def) && !tool.IsMCPCallDefinition(def) && def.Metadata[tool.MetadataToolKind] != codemode.ToolKind {
			if !codeModeCanCall(def) || !def.AvailableForModel(h.parent.RuntimeModel) {
				return reply, tool.NewError(tool.ErrorCodePermissionDenied, "tool is not callable from JavaScript")
			}
			h.mu.Lock()
			h.calls++
			over := h.calls > h.limits.Calls
			h.mu.Unlock()
			if over {
				return reply, tool.NewError(errorcode.ResourceExhausted, "search call budget exceeded")
			}
			return h.searchReply("", []codeModeSearchEntry{{def.Name, def.Description, def.InputSchema}}, false)
		}
	}
	search, available, err := h.owner.lookup(ctx, tool.ToolSearchToolName)
	if err != nil {
		return reply, err
	}
	if !available || search == nil || !tool.IsToolSearchDefinition(search.Definition()) {
		return reply, tool.NewError(tool.ErrorCodeNotFound, "no extension search source is configured")
	}
	args := map[string]any{"query": query, "limit": 4}
	if exact {
		args = map[string]any{"name": query}
	}
	raw, _ := json.Marshal(args)
	reply, err = h.call(context.WithValue(ctx, codeModeSearchKey{}, true), search.Definition().Name, raw, true)
	if err != nil || reply.IsError {
		return reply, err
	}
	var selected tool.ToolSearchResult
	for _, part := range reply.Parts {
		if part.Kind == "json" {
			var value map[string]any
			if json.Unmarshal(part.JSON, &value) == nil {
				selected = tool.ParseToolSearchOutput(value)
			}
			break
		}
	}
	if exact && len(selected.Tools) != 1 {
		return reply, tool.NewError(errorcode.ResourceExhausted, "complete tool schema is unavailable within the search budget; narrow the query")
	}
	entries := make([]codeModeSearchEntry, 0, len(selected.Tools))
	for _, entry := range selected.Tools {
		one, exists, lookupErr := h.owner.lookup(ctx, entry.Name)
		if lookupErr != nil {
			return reply, lookupErr
		}
		if !exists || one == nil || entry.Binding == nil {
			return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; search again before calling")
		}
		def := one.Definition()
		if def.Name != entry.Name || *entry.Binding != tool.BindingForDefinition(def) || !codeModeCanCall(def) || !def.AvailableForModel(h.parent.RuntimeModel) {
			return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; search again before calling")
		}
		h.mu.Lock()
		h.targets[entry.Name] = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
		h.discovered[entry.Name] = *entry.Binding
		h.mu.Unlock()
		entries = append(entries, codeModeSearchEntry{entry.Name, entry.Description, entry.InputSchema})
	}
	return h.searchReply(reply.CallID, entries, selected.Truncated)
}

// Schemas are complete or the search fails; no partial schema is advertised.
func (h *codeModeHost) searchReply(id string, entries []codeModeSearchEntry, truncated bool) (codemode.Reply, error) {
	raw, err := json.Marshal(map[string]any{"tools": entries, "count": len(entries), "truncated": truncated})
	if err != nil {
		return codemode.Reply{}, fmt.Errorf("encode search result: %w", err)
	}
	if len(raw)*6+len(id)*6+256 > h.limits.ReplyBytes {
		return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "complete search schemas exceed reply budget; narrow the query")
	}
	return codemode.Reply{CallID: id, Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}, nil
}
