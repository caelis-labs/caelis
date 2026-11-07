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

type codeModeDescribeKey struct{}

// Nested describe marks its result before the existing Journal records it.
// Search alone does not admit schemas. Canonical SDK ToolSearch still uses the
// same admission helper in Chat; there is no second durable binding store.
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
	if describe, _ := ctx.Value(codeModeDescribeKey{}).(bool); !describe {
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
			if tool.IsMCPDefinition(def) && def.AvailableForModel(call.RuntimeModel) {
				current[def.Name] = one
			}
		}
	}

	return mcpdiscovery.Admit(selected, current, result)
}

func codeModeCanCall(def tool.Definition) bool {
	// This is the real host-declared infrastructure requirement, retained by
	// aliases/wrappers. MCP domain exec does not acquire native CommandExec.
	return def.ExecutionRequirements == nil || !def.ExecutionRequirements.Sandbox.CommandExec
}

func (h *codeModeHost) Discover(ctx context.Context, query string, describe bool) (reply codemode.Reply, discoveryErr error) {
	before := 0
	h.mu.Lock()
	before = len(h.failures)
	closed := h.closed || h.ctx.Err() != nil
	h.mu.Unlock()
	defer func() {
		if discoveryErr != nil {
			h.mu.Lock()
			h.isError = true
			if len(h.failures) == before {
				h.failures = append(h.failures, map[string]string{"tool": "discovery", "outcome": codemode.OutcomeFailed, "code": string(errorcode.CodeOf(discoveryErr))})
			}
			h.mu.Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return codemode.Reply{}, err
	}
	if closed {
		return codemode.Reply{}, tool.NewError(errorcode.FailedPrecondition, "code mode host admission is closed")
	}

	if len(query) == 0 || len(query) > 1024 {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "invalid discovery query")
	}
	if describe {
		// Native tools are already registered by identity; their schema is known
		// without creating an MCP discovery receipt or executing the tool.
		one, found, lookupErr := h.owner.lookup(ctx, query)
		if lookupErr != nil {
			return codemode.Reply{}, lookupErr
		}
		if found && one != nil {
			def := one.Definition()
			if def.Name == query && !tool.IsMCPDefinition(def) && !tool.IsToolSearchDefinition(def) && !tool.IsMCPCallDefinition(def) && def.Metadata[tool.MetadataToolKind] != codemode.ToolKind {
				if !codeModeCanCall(def) {
					return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "native shell/process execution is prohibited from JavaScript")
				}
				h.mu.Lock()
				h.calls++
				over := h.calls > h.limits.Calls
				h.mu.Unlock()
				if over {
					return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "discovery call budget exceeded")
				}
				raw, err := json.Marshal(tool.NewToolSearchDiscoveredTool(def))
				return codemode.Reply{Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}, err
			}
		}
	}
	search, found, lookupErr := h.owner.lookup(ctx, tool.ToolSearchToolName)
	if lookupErr != nil {
		return codemode.Reply{}, lookupErr
	}
	if !found || search == nil || !tool.IsToolSearchDefinition(search.Definition()) {
		search = nil
	}
	if search == nil {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeNotFound, "no extension discovery source is configured")
	}
	args := map[string]any{"query": query}
	if describe {
		args = map[string]any{"name": query}
	}
	raw, _ := json.Marshal(args)
	reply, err := h.call(context.WithValue(ctx, codeModeDescribeKey{}, describe), search.Definition().Name, raw, true)
	if err != nil || reply.IsError {
		return reply, err
	}
	var result tool.ToolSearchResult
	for _, part := range reply.Parts {
		if part.Kind == "json" {
			var value map[string]any
			if json.Unmarshal(part.JSON, &value) == nil {
				result = tool.ParseToolSearchOutput(value)
			}
			break
		}
	}
	if describe {
		if len(result.Tools) != 1 || result.Tools[0].Name != query {
			return codemode.Reply{}, tool.NewError(tool.ErrorCodeNotFound, "tool is unavailable; search or describe again after the directory is ready")
		}
		entry := result.Tools[0]
		one, found, lookupErr := h.owner.lookup(ctx, query)
		if lookupErr != nil {
			return codemode.Reply{}, lookupErr
		}
		if found && one != nil {
			def := one.Definition()
			if def.Name == query && entry.Binding != nil && *entry.Binding == tool.BindingForDefinition(def) {
				if !codeModeCanCall(def) {
					return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "native shell/process execution is prohibited from JavaScript")
				}
				h.mu.Lock()
				h.targets[query] = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
				h.discovered[query] = *entry.Binding
				h.mu.Unlock()
				raw, _ = json.Marshal(entry)
				return codemode.Reply{CallID: reply.CallID, Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}, nil
			}
		}
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory refreshed; use tools.describe(name) to reload its schema/binding before calling")
	}
	type summary struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	names := make([]summary, 0, len(result.Tools))
	for _, entry := range result.Tools {
		names = append(names, summary{entry.Name, entry.Description})
	}
	raw, err = json.Marshal(map[string]any{"tools": names, "count": len(names), "truncated": result.Truncated})
	if err != nil {
		return codemode.Reply{}, fmt.Errorf("encode search summary: %w", err)
	}
	return codemode.Reply{CallID: reply.CallID, Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}, nil
}
