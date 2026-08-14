package eventstream

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// RelationDelivery is the resolved parent-tool relationship and delivery
// classification for one Envelope. Its values are copied from the Envelope or
// legacy metadata, so callers cannot mutate the source Envelope through it.
type RelationDelivery struct {
	ParentTool *ParentToolRelation
	Delivery   *Delivery
}

// ResolveRelationDelivery returns the authoritative relation and delivery
// classification for env. Typed Envelope fields win whenever their respective
// pointer is present. Only an absent typed pointer falls back to the legacy
// runtime-stream metadata layout. It never mutates env, its payload, or _meta.
func ResolveRelationDelivery(env Envelope) RelationDelivery {
	resolved := RelationDelivery{}
	if env.ParentTool != nil {
		parentTool := ParentToolRelation{
			ToolCallID: strings.TrimSpace(env.ParentTool.ToolCallID),
			ToolName:   strings.TrimSpace(env.ParentTool.ToolName),
		}
		resolved.ParentTool = &parentTool
	}
	if env.Delivery != nil {
		delivery := *env.Delivery
		resolved.Delivery = &delivery
	}
	if resolved.ParentTool != nil && resolved.Delivery != nil {
		return resolved
	}

	meta := metautil.Merge(UpdateMeta(env.Update), env.Meta)
	if resolved.ParentTool == nil {
		parentTool := ParentToolRelation{
			ToolCallID: metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamParentCallID),
			ToolName:   metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamParentTool),
		}
		if parentTool.ToolCallID != "" || parentTool.ToolName != "" {
			resolved.ParentTool = &parentTool
		}
	}
	if resolved.Delivery == nil {
		delivery := Delivery{}
		if metautil.Bool(meta, metautil.Root, metautil.Transient) {
			delivery.Mode = DeliveryTransient
		}
		if delivery.Mode != "" {
			resolved.Delivery = &delivery
		}
	}
	return resolved
}
