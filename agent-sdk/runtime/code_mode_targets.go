package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Search results and accepted ToolCalls share the canonical binding order.
// An accepted Code Mode intent retains its binding even if its outcome is unknown;
// reading that binding never replays the child.
func codeModeBindings(events []*session.Event) map[string]tool.Binding {
	bindings := map[string]tool.Binding{}
	for _, event := range events {
		applyMCPDiscoveryBindings(bindings, event)
		if event == nil || event.Type != session.EventTypeToolCall || event.Tool == nil {
			continue
		}
		record := event.Tool
		if record.Parent == nil || record.Parent.Kind != codemode.ToolKind || record.Binding == nil {
			continue
		}
		binding := *record.Binding
		if binding.Name == record.Name && binding.Identity != "" && binding.Version != "" {
			bindings[binding.Name] = binding
		}

	}
	return bindings
}

func (h *codeModeHost) eligibleTarget(one tool.Tool, def tool.Definition) bool {
	return def.Name != h.parent.Name && !mcp.IsSearchDefinition(def) &&
		!codemode.IsDefinition(def) && !toolbinding.HideViewImage(one, h.owner.producingModel) &&
		(!mcp.IsDefinition(def) || mcp.BindingForDefinition(def).Identity != "")
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
		if !h.eligibleTarget(one, def) {
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
		route := codeModeToolRoute(target.definition)
		route.Prefix = mcp.IsModelFunction(target.definition)
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })
	return routes, nil
}

func codeModeToolRoute(def tool.Definition) codemode.ToolRoute {
	route := codemode.ToolRoute{Name: def.Name}
	if mcp.IsDefinition(def) {
		route.Server, _ = def.Metadata[mcp.MetadataServer].(string)
		route.Tool, _ = def.Metadata[mcp.MetadataTool].(string)
		route.Plugin, _ = def.Metadata[mcp.MetadataPluginID].(string)
	}
	return route
}

func (h *codeModeHost) resolveTarget(ctx context.Context, name string, search bool) (codeModeTarget, error) {
	h.mu.Lock()
	target, found := h.targets[name]
	prior, known := h.bindings[name]
	h.mu.Unlock()
	stale := !found || mcp.IsDefinition(target.definition) && prior != mcp.BindingForDefinition(target.definition)
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
		if def.Name != name || (!search && !h.eligibleTarget(one, def)) || (search && !mcp.IsSearchDefinition(def)) {
			return codeModeTarget{}, tool.NewError(tool.ErrorCodeNotFound, "tool is not callable from JavaScript")
		}
		target = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
	}
	if !search && mcp.IsDefinition(target.definition) {
		binding := mcp.BindingForDefinition(target.definition)
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
