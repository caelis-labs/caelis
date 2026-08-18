package displaymodel

import "testing"

func TestRenderToolEventLineLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vm   ToolEventViewModel
		want string
	}{
		{
			name: "running",
			vm:   ToolEventViewModel{Name: "RunCommand", Args: "go test"},
			want: "▸ RunCommand go test",
		},
		{
			name: "expanded",
			vm:   ToolEventViewModel{Name: "RunCommand", Args: "go test", Expandable: true, Expanded: true},
			want: "▾ RunCommand go test",
		},
		{
			name: "done",
			vm:   ToolEventViewModel{Name: "Read", Done: true, Output: "README.md"},
			want: "✓ Read README.md",
		},
		{
			name: "done default",
			vm:   ToolEventViewModel{Name: "Read", Done: true},
			want: "✓ Read completed",
		},
		{
			name: "error",
			vm:   ToolEventViewModel{Name: "Patch", Done: true, Err: true, Output: "failed"},
			want: "✗ Patch failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RenderToolEventLine(tt.vm); got != tt.want {
				t.Fatalf("RenderToolEventLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolEventDisplayName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":            "TOOL",
		"execute":     "Execute",
		"think":       "Think",
		"custom tool": "custom tool",
	}
	for input, want := range tests {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if got := ToolEventDisplayName(input); got != want {
				t.Fatalf("ToolEventDisplayName(%q) = %q, want %q", input, got, want)
			}
		})
	}
}
