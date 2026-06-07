package providers

import "testing"

func TestToolArgsMapParsesObjectAndDefaultsEmptyToObject(t *testing.T) {
	got, err := toolArgsMap(`{"path":"/tmp","limit":10}`)
	if err != nil {
		t.Fatalf("toolArgsMap() error = %v", err)
	}
	if got["path"] != "/tmp" || got["limit"] != float64(10) {
		t.Fatalf("toolArgsMap() = %#v, want decoded object", got)
	}

	empty, err := toolArgsMap("")
	if err != nil {
		t.Fatalf("toolArgsMap(empty) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("toolArgsMap(empty) = %#v, want empty object", empty)
	}
}

func TestToolArgsMapRejectsNonObjectJSON(t *testing.T) {
	if _, err := toolArgsMap(`["not","object"]`); err == nil {
		t.Fatal("toolArgsMap(array) error = nil, want non-object error")
	}
}

func TestToolArgsRawUsesObjectDefault(t *testing.T) {
	if got := toolArgsRaw(nil); got != "{}" {
		t.Fatalf("toolArgsRaw(nil) = %q, want {}", got)
	}
	if got := toolArgsRaw(map[string]any{"path": "/tmp"}); got != `{"path":"/tmp"}` {
		t.Fatalf("toolArgsRaw(map) = %q, want object JSON", got)
	}
}
