package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

const codeModeCatalogKey = codemode.MetadataCatalog

// This is a hint snapshot, not discovery or authorization. An empty initial
// ready catalog stays empty; later tools remain discoverable without rewriting
// a Session's earlier instructions. Raw schemas never enter this snapshot.
func (r *Runtime) codeModeCatalog(ctx context.Context, ref session.SessionRef, source tool.Source, metadata map[string]any) (string, error) {
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return "", err
	}
	if text, ok := state[codeModeCatalogKey].(string); ok {
		return text, nil
	}
	type hint struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	selected := map[string]bool{}
	switch names := metadata[codemode.MetadataSelectedTools].(type) {
	case []string:
		for _, name := range names {
			selected[name] = true
		}
	case []any:
		for _, value := range names {
			if name, ok := value.(string); ok {
				selected[name] = true
			}
		}
	}
	var hints []hint
	var candidates []tool.Tool
	if len(selected) > 0 && source != nil {
		if named, ok := source.(tool.NamedSource); ok {
			names := make([]string, 0, len(selected))
			for name := range selected {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > 6 {
				names = names[:6]
			}
			for _, name := range names {
				one, found, lookupErr := named.Lookup(ctx, name)
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if lookupErr == nil && found {
					candidates = append(candidates, one)
				}
			}
		} else {
			candidates = source.Tools()
		}
	}
	for i, one := range candidates {
		if i >= 256 || len(hints) >= 6 {
			break
		}
		if one == nil {
			continue
		}
		def := one.Definition()
		if !tool.IsMCPDefinition(def) {
			continue
		}
		if !selected[def.Name] {
			continue
		}
		description := strings.TrimSpace(strings.TrimPrefix(def.Description, tool.ExternalCapabilityDescriptionPrefix))
		for len(description) > 128 {
			_, size := utf8.DecodeLastRuneInString(description)
			description = description[:len(description)-size]
		}
		hints = append(hints, hint{def.Name, description})
	}
	sort.Slice(hints, func(i, j int) bool { return hints[i].Name < hints[j].Name })
	if len(hints) > 6 {
		hints = hints[:6]
	}
	text := codemode.Guidance + "\n\n" + tool.ExternalCapabilityDescriptionPrefix
	if len(hints) > 0 {
		raw, _ := json.Marshal(hints)
		text += "\n\nPartial tool hint snapshot; use tools.search for other capabilities.\n" + string(raw)
	}
	_, err = r.sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Update: func(current map[string]any) (map[string]any, error) {
		if prior, ok := current[codeModeCatalogKey].(string); ok {
			text = prior
			return current, nil
		}
		if current == nil {
			current = map[string]any{}
		}
		current[codeModeCatalogKey] = text
		return current, nil
	}})
	return text, err
}
