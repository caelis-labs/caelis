package display

import names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"

// HydrateToolSummaryOutput copies model-visible output and fills display-only
// summary fields from runtime tool metadata. Existing output values win.
func HydrateToolSummaryOutput(name string, output map[string]any, meta map[string]any) map[string]any {
	out := cloneMap(output)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := runtimeToolMetadata(meta)
	if len(toolMeta) == 0 {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	info, ok := names.Lookup(name)
	if !ok {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	for _, key := range summaryMetadataKeys(info.ResultStyle) {
		if _, exists := out[key]; exists {
			continue
		}
		if value, exists := toolMeta[key]; exists {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func summaryMetadataKeys(style names.ResultStyle) []string {
	switch style {
	case names.ResultRead:
		return []string{"path", "file_path", "start_line", "end_line", "next_offset", "has_more"}
	case names.ResultList:
		return []string{"path", "count", "total_count"}
	case names.ResultGlob:
		return []string{"pattern", "count", "total_count"}
	case names.ResultSearch:
		return []string{"pattern", "query", "count", "file_count"}
	case names.ResultWebSearch:
		return []string{"query", "provider", "model", "status", "answer", "results", "message"}
	case names.ResultWebFetch:
		return []string{"url", "final_url", "title", "status", "status_code", "content_type", "format", "message"}
	default:
		return nil
	}
}

func runtimeToolMetadata(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
