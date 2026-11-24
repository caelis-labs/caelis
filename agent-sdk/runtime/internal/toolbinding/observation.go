package toolbinding

import (
	"context"
	"maps"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Observation belongs to one Runtime dispatch. Concurrent children, progress
// snapshots and a yielded script producer each receive their own observation.
type Observation struct {
	Discovery        bool
	ScriptCompletion string
	TaskEvent        map[string]any
	BuiltinDisplay   map[string]any
	MessageTo        string
	Source           *codemode.Source
}

type observationKey struct{}

// Observe starts an independent observation for one dispatch.
func Observe(ctx context.Context) (context.Context, *Observation) {
	o := new(Observation)
	return context.WithValue(ctx, observationKey{}, o), o
}

// Observed returns the dispatch observation. Standalone calls use a detached value.
func Observed(ctx context.Context) *Observation {
	if o, _ := ctx.Value(observationKey{}).(*Observation); o != nil {
		return o
	}
	return new(Observation)
}

// Apply records the declared result layout and admitted discovery on the Session fact.
func (o *Observation) Apply(fact *session.EventTool, def tool.Definition, success bool) {
	fact.Discovery = o.Discovery && success
}

// EventMeta builds the presentation data attached to the Session event.
func (o *Observation) EventMeta() map[string]any {
	meta := session.CloneState(o.TaskEvent)
	if meta == nil {
		meta = map[string]any{}
	}
	if o.BuiltinDisplay != nil {
		maps.Copy(RuntimeSection(meta, "tool"), o.BuiltinDisplay)
	}
	if o.MessageTo != "" {
		RuntimeSection(meta, "message")["to"] = o.MessageTo
	}
	if o.ScriptCompletion != "" {
		meta[codemode.MetadataScriptCompletion] = o.ScriptCompletion
	}
	if s := o.Source; s != nil {
		meta["code_mode_source"] = map[string]any{"path": s.Path, "sha256": s.SHA256, "code": s.Code}
	}
	return meta
}

// RuntimeSection returns the writable presentation section within an event.
func RuntimeSection(meta map[string]any, section string) map[string]any {
	if meta == nil {
		return nil
	}
	caelis, _ := meta["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
		meta["caelis"] = caelis
	}
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = 1
	}
	runtime, _ := caelis["runtime"].(map[string]any)
	if runtime == nil {
		runtime = map[string]any{}
		caelis["runtime"] = runtime
	}
	values, _ := runtime[section].(map[string]any)
	if values == nil {
		values = map[string]any{}
		runtime[section] = values
	}
	return values
}
