package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeDiscoveryTool struct {
	lookup func(context.Context, string) (tool.Tool, bool, error)
}

func (t codeModeDiscoveryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        tool.ToolSearchToolName,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}},
		Metadata:    map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch},
	}
}

func (t codeModeDiscoveryTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	var request struct{ Name string }
	if err := json.Unmarshal(call.Input, &request); err != nil {
		return tool.Result{}, err
	}
	one, ok, err := t.lookup(ctx, request.Name)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok || one == nil {
		return tool.Result{}, tool.NewError(errorcode.NotFound, "tool not found")
	}
	def := one.Definition()
	if def.Name != request.Name || !tool.IsMCPDefinition(def) || !def.AvailableForModel(call.RuntimeModel) {
		return tool.Result{}, tool.NewError(errorcode.NotFound, "tool not found")
	}
	selected := tool.NewToolSearchResult([]tool.Definition{def})
	if tool.EstimateToolSearchResultPromptTokens(selected) > tool.MaxToolSearchResultPromptTokens {
		return tool.Result{}, tool.NewError(errorcode.ResourceExhausted, "complete schema exceeds budget")
	}
	raw, err := json.Marshal(selected)
	return tool.Result{Content: []model.Part{model.NewJSONPart(raw)}, Metadata: map[string]any{mcpdiscovery.MetadataKey: true}}, err
}

type codeModeSearchEntry struct {
	codemode.ToolRoute
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	OutputFormat string         `json:"output_format"`
	CallPath     string         `json:"call_path"`
}

func codeModeSearchEntryFor(def tool.Definition) codeModeSearchEntry {
	name, _ := json.Marshal(def.Name)
	return codeModeSearchEntry{
		ToolRoute: codeModeToolRoute(def), Description: def.Description, InputSchema: def.InputSchema,
		CallPath: "tools.call(" + string(name) + ", args)", OutputSchema: def.OutputSchema, OutputFormat: "body",
	}
}

func (h *codeModeHost) Search(ctx context.Context, request codemode.SearchRequest) (reply codemode.Reply, discoveryErr error) {
	name := request.Name
	failureRecorded := false
	h.mu.Lock()
	closed := h.closed || h.ctx.Err() != nil
	h.mu.Unlock()
	defer func() {
		discoveryErr = mcpdiscovery.WithEntryHint(discoveryErr, "tools.search({name})")
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
	if err := request.Validate(); err != nil {
		return reply, err
	}
	if name == "" {
		return h.listTools(ctx, request)
	}
	one, found, err := h.owner.lookup(ctx, name)
	if err != nil {
		return reply, err
	}
	if !found || one == nil {
		return reply, tool.NewError(errorcode.NotFound, "tool not found")
	}
	def := one.Definition()
	if !h.eligibleTarget(def) || def.Name != name {
		return reply, tool.NewError(errorcode.NotFound, "tool not found")
	}
	if !tool.IsMCPDefinition(def) {
		if err := h.chargeDiscovery(); err != nil {
			return reply, err
		}
		return h.discoveryReply("", codeModeSearchEntryFor(def))
	}
	raw, _ := json.Marshal(map[string]string{"name": name})
	reply, err = h.call(ctx, tool.ToolSearchToolName, raw, true)
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
	if len(selected.Tools) != 1 {
		return reply, tool.NewError(errorcode.ResourceExhausted, "complete tool schema is unavailable within the search budget; narrow the query")
	}
	entry := selected.Tools[0]
	current, exists, err := h.owner.lookup(ctx, name)
	if err != nil {
		return reply, err
	}
	if !exists || current == nil || entry.Binding == nil {
		return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; binding is stale")
	}
	def = current.Definition()
	if entry.Name != name || def.Name != name || *entry.Binding != tool.BindingForDefinition(def) || !h.eligibleTarget(def) {
		return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; binding is stale")
	}
	h.mu.Lock()
	h.targets[name] = codeModeTarget{tool: current, definition: def, digest: codeModeDefinitionDigest(def)}
	h.bindings[name] = *entry.Binding
	h.mu.Unlock()
	return h.discoveryReply(reply.CallID, codeModeSearchEntryFor(def))
}

type toolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *codeModeHost) chargeDiscovery() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.calls > h.limits.Calls {
		return tool.NewError(errorcode.ResourceExhausted, "search call budget exceeded")
	}
	return nil
}
func (h *codeModeHost) listTools(ctx context.Context, request codemode.SearchRequest) (codemode.Reply, error) {
	if err := h.chargeDiscovery(); err != nil {
		return codemode.Reply{}, err
	}

	terms := strings.Fields(strings.ToLower(request.Query))
	entries := []toolSummary{}
	for _, one := range h.owner.availableTools() {
		if err := ctx.Err(); err != nil {
			return codemode.Reply{}, err
		}
		if one == nil {
			continue
		}
		def := one.Definition()
		if !tool.IsMCPDefinition(def) || !h.eligibleTarget(def) || def.Name <= request.After {
			continue
		}
		content := strings.ToLower(def.Name + " " + def.Description)
		if len(terms) > 0 && !slices.ContainsFunc(terms, func(term string) bool { return strings.Contains(content, term) }) {
			continue
		}
		entries = append(entries, toolSummary{def.Name, def.Description})
	}
	if h.owner.validate != nil {
		release, err := h.owner.validate(ctx)
		if release != nil {
			defer release()
		}
		if err != nil {
			return codemode.Reply{}, err
		}
	}
	slices.SortFunc(entries, func(left, right toolSummary) int { return strings.Compare(left.Name, right.Name) })
	var next *string
	const pageSize = 16
	if len(entries) > pageSize {
		entries = entries[:pageSize]
		next = &entries[pageSize-1].Name
	}
	return h.discoveryReply("", map[string]any{"tools": entries, "next": next})
}
func (h *codeModeHost) discoveryReply(id string, value any) (codemode.Reply, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return codemode.Reply{}, fmt.Errorf("encode search result: %w", err)
	}
	// Reserve fixed envelope overhead, then bound the ID and value using the
	// transport's conservative JSON escaping factor.
	const envelopeBytes = 256
	remaining := h.limits.ReplyBytes - envelopeBytes
	if remaining < 0 || len(id) > remaining/6 || len(raw) > remaining/6-len(id) {
		return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "complete search schemas exceed reply budget; narrow the query")
	}
	return codemode.Reply{CallID: id, Outcome: codemode.OutcomeSucceeded, Value: raw}, nil
}
