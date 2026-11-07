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

const codeModeCatalogKey = "code_mode_catalog"

// This is a hint snapshot, not discovery or authorization. An empty initial
// ready catalog stays empty; later tools remain discoverable without rewriting
// a Session's earlier instructions. Raw schemas never enter this snapshot.
func (r *Runtime) codeModeCatalog(ctx context.Context, ref session.SessionRef, candidates []tool.Tool, metadata map[string]any) (string, error) {
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
	for _, one := range candidates {
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
	text := `Native schema in tools: call directly. Known extension name: describe(name) once if schema is missing; otherwise call. Reuse valid schema across this Session's VMs/Turns; only JS variables reset. Unknown name: search(q) returns {tools:[{name,description}],count,truncated}; candidates grant no admission. describe takes a string:
const s=await tools.search("unknown capability");output.text(await tools.describe(s.tools[0].name));
Await calls sequentially. See schema before arguments; on stale, describe(name). Bindings/permissions stay live.`
	if len(hints) > 0 {
		raw, _ := json.Marshal(hints)
		text += "\n\nSelected hints (fixed, not grants). " + tool.ExternalCapabilityDescriptionPrefix + "\n" + string(raw)
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
