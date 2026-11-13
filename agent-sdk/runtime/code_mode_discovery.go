package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
)

type codeModeSearchKey struct{}
type codeModeModelAdmissionKey struct{}

// Nested search uses canonical admission before the existing Journal records
// full schemas/bindings. Ordinary native ToolSearch keeps its Chat owner.
type codeModeDiscoveryTool struct {
	base    tool.Tool
	lookup  func(context.Context, string) (tool.Tool, bool, error)
	runtime *Runtime
	active  session.Session
	turnID  string
	admit   func(context.Context, agent.ModelRequestAdmission) error
}

func (t codeModeDiscoveryTool) Definition() tool.Definition { return t.base.Definition() }
func (t codeModeDiscoveryTool) Call(ctx context.Context, call tool.Call) (returned tool.Result, callErr error) {
	nested, _ := ctx.Value(codeModeSearchKey{}).(bool)
	ctx = toolsearch.WithDiscoveryFilter(ctx, func(ctx context.Context, candidate tool.Definition) (bool, error) {
		one, found, err := t.lookup(ctx, candidate.Name)
		if err != nil {
			return false, tool.WrapError(errorcode.CodeOf(err), err, "tool source lookup failed")
		}
		if !found || one == nil {
			return false, nil
		}
		current := one.Definition()
		return tool.IsMCPDefinition(current) && current.AvailableForModel(call.RuntimeModel) && (!nested || codeModeCanCall(current)) && tool.BindingForDefinition(current) == tool.BindingForDefinition(candidate), nil
	})
	if nested {
		// The producer owns this sink through completion, including after the
		// foreground yields. Chat's per-step buffer cannot own late receipts.
		var mu sync.Mutex
		var attempts []model.Invocation
		ctx = toolsearch.WithInvocationAccounting(ctx, func(in model.Invocation) {
			mu.Lock()
			attempts = append(attempts, in)
			mu.Unlock()
		}, func(ctx context.Context, _ *model.Request) error {
			if t.admit == nil {
				return nil
			}
			return t.admit(ctx, agent.ModelRequestAdmission{RequestID: uuid.NewString(), TurnID: t.turnID})
		})
		defer func() {
			mu.Lock()
			records := append([]model.Invocation(nil), attempts...)
			mu.Unlock()
			active := t.active
			persistErr := t.runtime.persistModelInvocations(ctx, &active, t.turnID, records, "tool")
			if record, ok := ctx.Value(codeModeProducerErrorKey{}).(func(error)); ok && persistErr != nil {
				record(persistErr)
			}
			callErr = errors.Join(callErr, persistErr)
		}()
	}
	result, err := t.base.Call(ctx, call)
	if err != nil || result.IsError {
		return result, err
	}
	if !nested {
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
			if tool.IsMCPDefinition(def) && def.AvailableForModel(call.RuntimeModel) && codeModeCanCall(def) && entry.Binding != nil && *entry.Binding == tool.BindingForDefinition(def) {
				current[def.Name] = one
			}
		}
	}
	return mcpdiscovery.Admit(selected, current, result, call.Input)
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

func (h *codeModeHost) Search(ctx context.Context, request codemode.SearchRequest) (reply codemode.Reply, discoveryErr error) {
	query := request.Query
	service := request.Server != ""
	failureRecorded := false
	h.mu.Lock()
	closed := h.closed || h.ctx.Err() != nil
	h.mu.Unlock()
	defer func() {
		discoveryErr = mcpdiscovery.WithEntryHint(discoveryErr, "tools.search(name)")
		if discoveryErr != nil {
			h.mu.Lock()
			h.isError = true
			if !failureRecorded {
				h.failures = append(h.failures, codeModeFailure{Tool: "search", Outcome: codemode.OutcomeFailed, Code: string(errorcode.CodeOf(discoveryErr))})
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
	if service && (query != "" || len(request.Server) > 1024 || len(request.Plugin) > 1024) || !service && (len(query) == 0 || len(query) > 1024 || request.Plugin != "" || request.After != "") || len(request.After) > 1024 {
		return reply, tool.NewError(tool.ErrorCodeInvalidInput, "supply a query or an exact server and optional plugin")
	}
	var one tool.Tool
	var found bool
	var err error
	if !service {
		one, found, err = h.owner.lookup(ctx, query)
		if err != nil {
			return reply, err
		}
	}
	exact := false
	if found && one != nil {
		def := one.Definition()
		exact = def.Name == query && tool.IsMCPDefinition(def)
		if def.Name == query && !tool.IsMCPDefinition(def) && !tool.IsToolSearchDefinition(def) && !tool.IsMCPCallDefinition(def) && def.Metadata[tool.MetadataToolKind] != codemode.ToolKind {
			if !codeModeCanCall(def) || !def.AvailableForModel(h.parent.RuntimeModel) {
				return reply, codemode.NewPublicPermissionError("tool is not callable from JavaScript")
			}
			h.mu.Lock()
			h.calls++
			over := h.calls > h.limits.Calls
			h.mu.Unlock()
			if over {
				return reply, tool.NewError(errorcode.ResourceExhausted, "search call budget exceeded")
			}
			return h.searchReply("", []codeModeSearchEntry{{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema}}, false, 0, "")
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
	} else if service {
		args = map[string]any{"server": request.Server, "limit": 16}
		if request.Plugin != "" {
			args["plugin"] = request.Plugin
		}
		if request.After != "" {
			args["after"] = request.After
		}
	}
	raw, _ := json.Marshal(args)
	reply, err = h.call(context.WithValue(ctx, codeModeSearchKey{}, true), search.Definition().Name, raw, true)
	failureRecorded = err != nil || reply.IsError || reply.Outcome == codemode.OutcomeUnknown
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
			return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; binding is stale")
		}
		def := one.Definition()
		if def.Name != entry.Name || *entry.Binding != tool.BindingForDefinition(def) || !codeModeCanCall(def) || !def.AvailableForModel(h.parent.RuntimeModel) {
			return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; binding is stale")
		}
		h.mu.Lock()
		h.targets[entry.Name] = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
		h.discovered[entry.Name] = *entry.Binding
		h.mu.Unlock()
		entries = append(entries, codeModeSearchEntry{Name: entry.Name, Description: entry.Description, InputSchema: entry.InputSchema})
	}
	return h.searchReply(reply.CallID, entries, selected.Truncated, selected.OmittedCount, selected.Next)
}

// Schemas are complete or the search fails; no partial schema is advertised.
func (h *codeModeHost) searchReply(id string, entries []codeModeSearchEntry, truncated bool, omitted int, next string) (codemode.Reply, error) {
	raw, err := json.Marshal(struct {
		Tools        []codeModeSearchEntry `json:"tools"`
		Count        int                   `json:"count"`
		Truncated    bool                  `json:"truncated"`
		OmittedCount int                   `json:"omitted_count,omitempty"`
		Next         string                `json:"next,omitempty"`
	}{entries, len(entries), truncated, omitted, next})
	if err != nil {
		return codemode.Reply{}, fmt.Errorf("encode search result: %w", err)
	}
	if len(raw)*6+len(id)*6+256 > h.limits.ReplyBytes {
		return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "complete search schemas exceed reply budget; narrow the query")
	}
	return codemode.Reply{CallID: id, Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}, nil
}
