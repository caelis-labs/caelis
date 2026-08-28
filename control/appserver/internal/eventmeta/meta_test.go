package eventmeta

import (
	"reflect"
	"testing"
)

func TestWithoutRuntimeSectionKeysClonesAndRetainsSiblings(t *testing.T) {
	meta := map[string]any{
		"provider": map[string]any{"trace": []any{"a"}},
		Root: map[string]any{
			"version": 1,
			Runtime: map[string]any{
				RuntimeTask: map[string]any{
					RuntimeTaskTerminalID: "terminal-1",
					RuntimeOutputDelta:    "duplicate",
				},
				RuntimeStream: map[string]any{RuntimeStreamMode: "canonical"},
			},
		},
	}

	got := WithoutRuntimeSectionKeys(meta, RuntimeTask, RuntimeOutputDelta)
	want := map[string]any{
		"provider": map[string]any{"trace": []any{"a"}},
		Root: map[string]any{
			"version": 1,
			Runtime: map[string]any{
				RuntimeTask: map[string]any{
					RuntimeTaskTerminalID: "terminal-1",
				},
				RuntimeStream: map[string]any{RuntimeStreamMode: "canonical"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutRuntimeSectionKeys() = %#v, want %#v", got, want)
	}

	got["provider"].(map[string]any)["trace"].([]any)[0] = "changed"
	got[Root].(map[string]any)[Runtime].(map[string]any)[RuntimeTask].(map[string]any)[RuntimeTaskTerminalID] = "changed"
	if !reflect.DeepEqual(meta["provider"], map[string]any{"trace": []any{"a"}}) ||
		meta[Root].(map[string]any)[Runtime].(map[string]any)[RuntimeTask].(map[string]any)[RuntimeTaskTerminalID] != "terminal-1" {
		t.Fatalf("input metadata mutated: %#v", meta)
	}
}

func TestWithoutRuntimeSectionKeysRemovesEmptyParents(t *testing.T) {
	meta := map[string]any{
		Root: map[string]any{
			Runtime: map[string]any{
				RuntimeTask: map[string]any{RuntimeOutputDelta: "duplicate"},
			},
		},
	}
	if got := WithoutRuntimeSectionKeys(meta, RuntimeTask, RuntimeOutputDelta); got != nil {
		t.Fatalf("WithoutRuntimeSectionKeys() = %#v, want nil", got)
	}
}
