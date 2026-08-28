package eventmeta

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestWithoutRuntimeSectionKeysClonesAndRetainsSiblings(t *testing.T) {
	meta := map[string]any{
		"provider": map[string]any{"trace": []any{"a"}},
		metautil.Root: map[string]any{
			"version": 1,
			metautil.Runtime: map[string]any{
				metautil.RuntimeTask: map[string]any{
					metautil.RuntimeTaskTerminalID: "terminal-1",
					metautil.RuntimeOutputDelta:    "duplicate",
				},
				metautil.RuntimeStream: map[string]any{metautil.RuntimeStreamMode: "canonical"},
			},
		},
	}

	got := WithoutRuntimeSectionKeys(meta, metautil.RuntimeTask, metautil.RuntimeOutputDelta)
	want := map[string]any{
		"provider": map[string]any{"trace": []any{"a"}},
		metautil.Root: map[string]any{
			"version": 1,
			metautil.Runtime: map[string]any{
				metautil.RuntimeTask: map[string]any{
					metautil.RuntimeTaskTerminalID: "terminal-1",
				},
				metautil.RuntimeStream: map[string]any{metautil.RuntimeStreamMode: "canonical"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutRuntimeSectionKeys() = %#v, want %#v", got, want)
	}

	got["provider"].(map[string]any)["trace"].([]any)[0] = "changed"
	got[metautil.Root].(map[string]any)[metautil.Runtime].(map[string]any)[metautil.RuntimeTask].(map[string]any)[metautil.RuntimeTaskTerminalID] = "changed"
	if !reflect.DeepEqual(meta["provider"], map[string]any{"trace": []any{"a"}}) ||
		meta[metautil.Root].(map[string]any)[metautil.Runtime].(map[string]any)[metautil.RuntimeTask].(map[string]any)[metautil.RuntimeTaskTerminalID] != "terminal-1" {
		t.Fatalf("input metadata mutated: %#v", meta)
	}
}

func TestWithoutRuntimeSectionKeysRemovesEmptyParents(t *testing.T) {
	meta := map[string]any{
		metautil.Root: map[string]any{
			metautil.Runtime: map[string]any{
				metautil.RuntimeTask: map[string]any{metautil.RuntimeOutputDelta: "duplicate"},
			},
		},
	}
	if got := WithoutRuntimeSectionKeys(meta, metautil.RuntimeTask, metautil.RuntimeOutputDelta); got != nil {
		t.Fatalf("WithoutRuntimeSectionKeys() = %#v, want nil", got)
	}
}
