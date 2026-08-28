// Package eventmeta contains metadata shaping shared only by Control AppServer
// feed projection and replay.
package eventmeta

import "github.com/caelis-labs/caelis/protocol/acp/metautil"

// WithoutRuntimeSectionKeys returns an isolated metadata map without selected
// keys from one _meta.caelis.runtime section. Empty runtime and Caelis maps are
// removed while unrelated provider metadata and sibling sections are retained.
func WithoutRuntimeSectionKeys(meta map[string]any, section string, keys ...string) map[string]any {
	out := metautil.CloneMap(meta)
	if len(out) == 0 || section == "" || len(keys) == 0 {
		return out
	}
	caelis := metautil.CloneMap(mapAt(out, metautil.Root))
	runtime := metautil.CloneMap(mapAt(caelis, metautil.Runtime))
	sectionMap := metautil.CloneMap(mapAt(runtime, section))
	if len(sectionMap) == 0 {
		return out
	}
	for _, key := range keys {
		delete(sectionMap, key)
	}
	if len(sectionMap) == 0 {
		delete(runtime, section)
	} else {
		runtime[section] = sectionMap
	}
	if len(runtime) == 0 {
		delete(caelis, metautil.Runtime)
	} else {
		caelis[metautil.Runtime] = runtime
	}
	if len(caelis) == 0 {
		delete(out, metautil.Root)
	} else {
		out[metautil.Root] = caelis
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapAt(values map[string]any, key string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out, _ := values[key].(map[string]any)
	return out
}
