package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type codeModeDiscoveryTool struct {
	lookup       func(context.Context, string) (tool.Tool, bool, error)
	checkBinding func(context.Context, tool.Binding) error
}

func (t codeModeDiscoveryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        mcp.SearchToolName,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}},
		Metadata:    map[string]any{mcp.MetadataKind: mcp.SearchToolName},
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
	if def.Name != request.Name || !mcp.IsDefinition(def) {
		return tool.Result{}, tool.NewError(errorcode.NotFound, "tool not found")
	}
	if mcp.IsModelFunction(def) && t.checkBinding != nil {
		if err := t.checkBinding(ctx, mcp.BindingForDefinition(def)); err != nil {
			return tool.Result{}, err
		}
	}
	selected := mcp.NewToolSearchResult([]tool.Definition{def})
	if mcp.EstimateToolSearchResultPromptTokens(selected) > mcp.MaxToolSearchResultPromptTokens {
		return tool.Result{}, tool.NewError(errorcode.ResourceExhausted, "complete schema exceeds budget")
	}
	raw, err := json.Marshal(selected)
	toolbinding.Observed(ctx).Discovery = err == nil
	return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, err
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
	return codeModeSearchEntry{
		ToolRoute: codeModeToolRoute(def), Description: def.Description, InputSchema: def.InputSchema,
		CallPath: codeModeCallPath(def.Name), OutputSchema: def.OutputSchema, OutputFormat: "body",
	}
}

func codeModeCallPath(name string) string {
	raw, _ := json.Marshal(name)
	return "tools.call(" + string(raw) + ", args)"
}

func (h *codeModeHost) Search(ctx context.Context, request codemode.SearchRequest) (reply codemode.Reply, discoveryErr error) {
	name := request.Name
	failureRecorded := false
	h.mu.Lock()
	closed := h.closed || h.ctx.Err() != nil
	h.mu.Unlock()
	defer func() {
		discoveryErr = withMCPDiscoveryEntryHint(discoveryErr, "tools.search({name})")
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
	if request.Names != nil {
		return h.searchNames(ctx, request.Names)
	}
	if name == "" {
		return h.listTools(ctx, request)
	}
	return h.searchName(ctx, name, h.discoveryValueBudget(), &failureRecorded)
}

func (h *codeModeHost) searchName(ctx context.Context, name string, budget int, failureRecorded *bool) (reply codemode.Reply, err error) {
	one, found, err := h.owner.lookup(ctx, name)
	if err != nil || !found || one == nil {
		if chargeErr := h.chargeDiscovery(); chargeErr != nil {
			return reply, chargeErr
		}
		if err != nil {
			return reply, err
		}
		return reply, tool.NewError(errorcode.NotFound, "tool not found")
	}
	def := one.Definition()
	if !h.eligibleTarget(one, def) || def.Name != name {
		if err := h.chargeDiscovery(); err != nil {
			return reply, err
		}
		return reply, tool.NewError(errorcode.NotFound, "tool not found")
	}
	if mcp.IsModelFunction(def) && h.owner.checkBinding != nil {
		if err := h.owner.checkBinding(ctx, mcp.BindingForDefinition(def)); err != nil {
			if chargeErr := h.chargeDiscovery(); chargeErr != nil {
				return reply, chargeErr
			}
			return reply, err
		}
	}
	entryJSON, err := json.Marshal(codeModeSearchEntryFor(def))
	if err != nil {
		return reply, err
	}
	remote := mcp.IsDefinition(def)
	overBudget := len(entryJSON) > budget || remote && failureRecorded == nil &&
		mcp.EstimateToolSearchResultPromptTokens(mcp.NewToolSearchResult([]tool.Definition{def})) > mcp.MaxToolSearchResultPromptTokens
	if overBudget || !remote {
		if err := h.chargeDiscovery(); err != nil {
			return reply, err
		}
		if overBudget {
			return reply, tool.NewError(errorcode.ResourceExhausted, "complete schema exceeds search budget")
		}
		return h.discoveryReply("", json.RawMessage(entryJSON))
	}
	raw, _ := json.Marshal(map[string]string{"name": name})
	reply, err = h.call(ctx, mcp.SearchToolName, raw, true)
	if failureRecorded != nil {
		*failureRecorded = err != nil || reply.IsError || reply.Outcome == codemode.OutcomeUnknown
	}
	if err != nil || reply.IsError {
		return reply, err
	}
	var selected mcp.ToolSearchResult
	for _, part := range reply.Parts {
		if part.Kind == "json" {
			var value map[string]any
			if json.Unmarshal(part.JSON, &value) == nil {
				selected = mcp.ParseToolSearchOutput(value)
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
	if entry.Name != name || def.Name != name || *entry.Binding != mcp.BindingForDefinition(def) || !h.eligibleTarget(current, def) {
		return reply, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory changed; binding is stale")
	}
	h.mu.Lock()
	h.targets[name] = codeModeTarget{tool: current, definition: def, digest: codeModeDefinitionDigest(def)}
	h.bindings[name] = *entry.Binding
	h.mu.Unlock()
	return h.discoveryReply(reply.CallID, codeModeSearchEntryFor(def))
}

type codeModeSearchItem struct {
	Name  string          `json:"name"`
	Entry json.RawMessage `json:"entry,omitempty"`
	Error errorcode.Code  `json:"error_code,omitempty"`
}

func (h *codeModeHost) searchNames(ctx context.Context, names []string) (codemode.Reply, error) {
	items := make([]codeModeSearchItem, len(names))
	for i, name := range names {
		items[i] = codeModeSearchItem{Name: name, Error: errorcode.ResourceExhausted}
	}
	page := map[string]any{"tools": items}
	for i, name := range names {
		h.mu.Lock()
		exhausted := h.calls >= h.limits.Calls
		h.mu.Unlock()
		if exhausted {
			break
		}
		initial, _ := json.Marshal(page)
		budget := h.discoveryValueBudget() - len(initial)
		if budget <= 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return codemode.Reply{}, err
		}
		reply, err := h.searchName(ctx, name, budget, nil)
		if err == nil && !reply.IsError {
			if len(reply.Value) <= budget {
				items[i].Entry, items[i].Error = reply.Value, ""
			}
			continue
		}
		if err == nil {
			return codemode.Reply{}, tool.NewError(errorcode.FailedPrecondition, "tool discovery failed")
		}
		h.mu.Lock()
		fatal := h.fatal
		h.mu.Unlock()
		if fatal != nil {
			return codemode.Reply{}, fatal
		}
		switch code := errorcode.CodeOf(err); code {
		case errorcode.NotFound, tool.ErrorCodeStaleRevision, errorcode.Unavailable, errorcode.PermissionDenied, errorcode.ResourceExhausted:
			items[i].Error = code
		default:
			return codemode.Reply{}, err
		}
	}
	return h.discoveryReply("", page)
}

type toolSummary struct {
	codemode.ToolRoute
	Description string `json:"description"`
	CallPath    string `json:"call_path"`
	score       int
}

func (h *codeModeHost) chargeDiscovery() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.calls >= h.limits.Calls {
		return tool.NewError(errorcode.ResourceExhausted, "search call budget exceeded")
	}
	h.calls++
	return nil
}
func (h *codeModeHost) listTools(ctx context.Context, request codemode.SearchRequest) (codemode.Reply, error) {
	if err := h.chargeDiscovery(); err != nil {
		return codemode.Reply{}, err
	}

	terms := searchWords(request.Query)
	entries := []toolSummary{}
	for _, one := range h.owner.availableTools() {
		if err := ctx.Err(); err != nil {
			return codemode.Reply{}, err
		}
		if one == nil {
			continue
		}
		def := one.Definition()
		if !h.eligibleTarget(one, def) || (len(terms) == 0 && def.Name <= request.After) {
			continue
		}
		if mcp.IsModelFunction(def) && h.owner.checkBinding != nil &&
			h.owner.checkBinding(ctx, mcp.BindingForDefinition(def)) != nil {
			continue
		}
		route := codeModeToolRoute(def)
		score, matched := searchToolScore(route, def.Description, terms)
		if !matched {
			continue
		}
		description := def.Description
		if len(description) > 160 {
			end := 160
			for end > 0 && !utf8.RuneStart(description[end]) {
				end--
			}
			description = description[:end] + "…"
		}
		entries = append(entries, toolSummary{ToolRoute: route, Description: description, CallPath: codeModeCallPath(def.Name), score: score})
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
	slices.SortFunc(entries, func(left, right toolSummary) int {
		if left.score != right.score {
			return right.score - left.score
		}
		return strings.Compare(left.Name, right.Name)
	})
	if len(terms) != 0 && request.After != "" {
		prior := slices.IndexFunc(entries, func(entry toolSummary) bool { return entry.Name == request.After })
		if prior < 0 {
			return codemode.Reply{}, tool.NewError(tool.ErrorCodeStaleRevision, "search page changed; start again without after")
		}
		entries = entries[prior+1:]
	}
	const pageSize = 16
	for size := min(pageSize, len(entries)); ; size-- {
		var next *string
		if size < len(entries) && size > 0 {
			next = &entries[size-1].Name
		}
		reply, err := h.discoveryReply("", map[string]any{"tools": entries[:size], "next": next})
		if err == nil || size <= 1 || !errorcode.Is(err, errorcode.ResourceExhausted) {
			return reply, err
		}
	}
}

// Reserve fixed envelope overhead and the bridge's worst-case escaping factor.
func (h *codeModeHost) discoveryValueBudget() int { return (h.limits.ReplyBytes - 256) / 6 }

func (h *codeModeHost) discoveryReply(id string, value any) (codemode.Reply, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return codemode.Reply{}, fmt.Errorf("encode search result: %w", err)
	}
	if len(id)+len(raw) > h.discoveryValueBudget() {
		return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "complete search schemas exceed reply budget; narrow the query")
	}
	return codemode.Reply{CallID: id, Outcome: codemode.OutcomeSucceeded, Value: raw}, nil
}
