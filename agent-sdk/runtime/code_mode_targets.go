package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Static registration owns its canonical name, as in model-request lookup.
// Deferred tools have already received the same Runtime wrappers.
func codeModeAvailableTools(static []tool.Tool, source tool.Source) []tool.Tool {
	items := append([]tool.Tool(nil), static...)
	names := map[string]bool{}
	for _, item := range static {
		if item != nil {
			names[item.Definition().Name] = true
		}
	}
	if source != nil {
		for _, item := range source.Tools() {
			if item != nil && !names[item.Definition().Name] {
				items = append(items, item)
			}
		}
	}
	return items
}

// Search and actual execution receipts share the existing canonical history.
// A started Code Mode child pins its binding even if its outcome is unknown;
// reading that binding never replays the child or grants legacy mcp_call access.
func codeModeBindings(events []*session.Event) map[string]tool.Binding {
	bindings := map[string]tool.Binding{}
	for _, event := range events {
		mcpdiscovery.Apply(bindings, event)
		if event == nil || !session.IsJournal(event) || event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		record := event.Journal.ToolExecution
		if record.Parent == nil || record.Parent.Kind != codemode.ToolKind || record.Status != session.ToolExecutionStarted || record.Binding == nil {
			continue
		}
		binding := *record.Binding
		if binding.Name == record.ToolName && binding.Identity != "" && binding.Version != "" {
			bindings[binding.Name] = binding
		}
	}
	return bindings
}

func (h *codeModeHost) eligibleTarget(def tool.Definition) bool {
	return def.Name != h.parent.Name && !tool.IsMCPCallDefinition(def) && !tool.IsToolSearchDefinition(def) &&
		def.Metadata[tool.MetadataToolKind] != codemode.ToolKind && def.AvailableForModel(h.parent.RuntimeModel) &&
		(!tool.IsMCPDefinition(def) || tool.BindingForDefinition(def).Identity != "")
}

func (h *codeModeHost) prepareTargets(ctx context.Context, events []*session.Event) error {
	h.bindings = codeModeBindings(events)
	for _, one := range h.owner.availableTools() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if one == nil {
			continue
		}
		def := one.Definition()
		if !h.eligibleTarget(def) {
			continue
		}
		if _, duplicate := h.targets[def.Name]; duplicate {
			return fmt.Errorf("duplicate Code Mode target %q", def.Name)
		}
		h.targets[def.Name] = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
	}
	return nil
}

// ToolNames returns only presentation routes. Availability, the pinned version,
// schema and approval are checked on every call through the same Runtime Host.
func (h *codeModeHost) ToolNames(ctx context.Context) ([]codemode.ToolRoute, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	routes := make([]codemode.ToolRoute, 0, len(h.targets))
	for _, target := range h.targets {
		def := target.definition
		if !codeModeCanCall(def) {
			continue
		}
		routes = append(routes, codeModeToolRoute(def))
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })
	return routes, nil
}

func codeModeToolRoute(def tool.Definition) codemode.ToolRoute {
	route := codemode.ToolRoute{Name: def.Name}
	if tool.IsMCPDefinition(def) {
		route.Server, _ = def.Metadata[tool.MetadataMCPServer].(string)
		route.Tool, _ = def.Metadata[tool.MetadataMCPTool].(string)
		route.Plugin, _ = def.Metadata[tool.MetadataPluginID].(string)
	}
	return route
}

func (h *codeModeHost) resolveTarget(ctx context.Context, name string, search bool) (codeModeTarget, error) {
	h.mu.Lock()
	target, found := h.targets[name]
	prior, known := h.bindings[name]
	h.mu.Unlock()
	stale := !found || tool.IsMCPDefinition(target.definition) && prior != tool.BindingForDefinition(target.definition)
	if !search && known && stale && h.owner.checkBinding != nil {
		if err := h.owner.checkBinding(ctx, prior); err != nil {
			return codeModeTarget{}, err
		}
	}
	if !found || search {
		one, exists, err := h.owner.lookup(ctx, name)
		if err != nil {
			return codeModeTarget{}, err
		}
		if !exists || one == nil {
			if known {
				return codeModeTarget{}, tool.NewError(errorcode.Unavailable, "bound tool is currently unavailable; check connection or access")
			}
			return codeModeTarget{}, tool.NewError(tool.ErrorCodeNotFound, "tool is not available in this Session")
		}
		def := one.Definition()
		if def.Name != name || (!search && !h.eligibleTarget(def)) || (search && !tool.IsToolSearchDefinition(def)) {
			return codeModeTarget{}, tool.NewError(tool.ErrorCodeNotFound, "tool is not callable from JavaScript")
		}
		target = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
	}
	if !search && tool.IsMCPDefinition(target.definition) {
		binding := tool.BindingForDefinition(target.definition)
		h.mu.Lock()
		defer h.mu.Unlock()
		// Re-read after lookup: concurrent search/calls cannot silently replace a
		// pinned version. Search is the explicit schema/binding refresh boundary.
		if prior, known := h.bindings[name]; known && prior != binding {
			return codeModeTarget{}, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory refreshed; binding is stale")
		}
		h.bindings[name] = binding
		h.targets[name] = target
	}
	return target, nil
}
