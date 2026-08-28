package client

import "github.com/caelis-labs/caelis/internal/jsonvalue"

const (
	displayMetaRootKey        = "caelis"
	displayMetaVersionKey     = "version"
	displayMetaSectionKey     = "display"
	displayMetaToolInputKey   = "tool_input"
	displayMetaExplorationKey = "exploration_verb"
)

func withDisplayExplorationVerb(meta map[string]any, verb string) map[string]any {
	return withDisplayMetaValues(meta, map[string]any{displayMetaExplorationKey: verb})
}

func withoutDisplayExplorationVerb(meta map[string]any) map[string]any {
	return withoutDisplayMetaKeys(meta, displayMetaExplorationKey)
}

// WithDisplayToolInput adds the normalized safe input recovered by the
// Host-private external ACP compatibility adapter.
func WithDisplayToolInput(meta map[string]any, input map[string]any) map[string]any {
	if len(input) == 0 {
		return jsonvalue.CloneMap(meta)
	}
	return withDisplayMetaValues(meta, map[string]any{displayMetaToolInputKey: input})
}

// WithoutDisplayToolInput removes untrusted or stale recovered tool input
// while preserving unrelated provider and display metadata.
func WithoutDisplayToolInput(meta map[string]any) map[string]any {
	return withoutDisplayMetaKeys(meta, displayMetaToolInputKey)
}

func withDisplayMetaValues(meta map[string]any, values map[string]any) map[string]any {
	if len(values) == 0 {
		return jsonvalue.CloneMap(meta)
	}
	return jsonvalue.MergeMap(meta, map[string]any{
		displayMetaRootKey: map[string]any{
			displayMetaVersionKey: 1,
			displayMetaSectionKey: values,
		},
	})
}

func withoutDisplayMetaKeys(meta map[string]any, keys ...string) map[string]any {
	out := jsonvalue.CloneMap(meta)
	if len(out) == 0 || len(keys) == 0 {
		return out
	}
	caelis := jsonvalue.CloneMap(displayMetaMapAt(out, displayMetaRootKey))
	display := jsonvalue.CloneMap(displayMetaMapAt(caelis, displayMetaSectionKey))
	if len(display) == 0 {
		return out
	}
	for _, key := range keys {
		delete(display, key)
	}
	caelis[displayMetaSectionKey] = display
	out[displayMetaRootKey] = caelis
	return out
}

func displayMetaMapAt(values map[string]any, key string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out, _ := values[key].(map[string]any)
	return out
}
