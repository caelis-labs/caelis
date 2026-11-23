package runtime

import (
	"context"
	"slices"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// bindRequestTools freezes model-facing tools and installs the live script
// directory only for requests that declare Code Mode.
func (r *Runtime) bindRequestTools(ctx context.Context, active session.Session, ref session.SessionRef, spec *agent.AgentSpec, snapshot *agent.ModelRequestSnapshot, wrap func([]tool.Tool) []tool.Tool) error {
	source := spec.DeferredTools
	var directory codeModeDirectory
	if source != nil {
		directory.deferred = &deferredToolSource{source: source, wrap: wrap}
		spec.DeferredTools = directory.deferred
	}
	var discovery *codeModeDiscoveryTool
	for i, one := range spec.Tools {
		declaration, ok := one.(*codemode.Tool)
		if !ok {
			continue
		}
		if declaration.Name == codemode.ToolName {
			catalog, err := r.codeModeCatalog(ctx, ref, source, spec.Metadata)
			if err != nil {
				return err
			}
			if spec.Metadata == nil {
				spec.Metadata = map[string]any{}
			}
			spec.Metadata[codemode.MetadataCatalog] = catalog
		}
		if discovery == nil {
			discovery = &codeModeDiscoveryTool{lookup: directory.lookup}
			if checker, ok := source.(interface {
				CheckToolBinding(context.Context, tool.Binding) error
			}); ok {
				discovery.checkBinding = checker.CheckToolBinding
			}
		}
		spec.Tools[i] = runtimeCodeModeTool{
			runtime: r, tasks: r.tasks, active: session.CloneSession(active), sessions: r.sessions, ref: ref, declaration: declaration,
			lookup: directory.lookup, availableTools: directory.available, checkBinding: discovery.checkBinding, validate: codeModeValidator(snapshot),
		}
	}
	if snapshot != nil {
		if catalog, _ := spec.Metadata[codemode.MetadataCatalog].(string); catalog != "" {
			snapshot.Instructions = append(snapshot.Instructions, model.NewTextPart(catalog))
		}
	}
	if discovery != nil {
		spec.Tools = slices.DeleteFunc(spec.Tools, func(t tool.Tool) bool { return t == nil || mcp.IsSearchDefinition(t.Definition()) })
	}
	spec.Tools = wrap(spec.Tools)
	if discovery == nil {
		return nil
	}
	for _, t := range spec.Tools {
		// Direct functions keep the model's frozen binding. Script calls must
		// resolve the current MCP target when the host supplied a live source.
		if source == nil || !mcp.IsModelFunction(t.Definition()) {
			directory.local = append(directory.local, t)
		}
	}
	directory.local = append(directory.local, wrap([]tool.Tool{discovery})...)
	return nil
}

func codeModeValidator(snapshot *agent.ModelRequestSnapshot) func(context.Context) (func(), error) {
	if snapshot == nil {
		return nil
	}
	if snapshot.Validate != nil {
		return snapshot.Validate
	}
	return func(context.Context) (func(), error) {
		return nil, tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode host calls require a dynamic snapshot generation validator")
	}
}

// A directory belongs to one prepared request. Both discovery and dispatch
// resolve wrapped local tools first and share the same deferred source.
type codeModeDirectory struct {
	local    []tool.Tool
	deferred *deferredToolSource
}

func (d *codeModeDirectory) lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	for _, t := range d.local {
		if t.Definition().Name == name {
			return t, true, nil
		}
	}
	if d.deferred != nil {
		return d.deferred.Lookup(ctx, name)
	}
	return nil, false, nil
}

func (d *codeModeDirectory) available() []tool.Tool {
	if d.deferred == nil {
		return d.local
	}
	items := slices.Clone(d.local)
	localNames := make(map[string]bool, len(items))
	for _, t := range d.local {
		localNames[t.Definition().Name] = true
	}
	for _, t := range d.deferred.Tools() {
		if !localNames[t.Definition().Name] {
			items = append(items, t)
		}
	}
	return items
}
