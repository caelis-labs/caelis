package tuiapp

import "testing"

func TestCompactPathDisplayWithBaseHandlesWindowsPaths(t *testing.T) {
	t.Parallel()

	base := `D:\xue\code\storage`
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "workspace root",
			path: base,
			want: "storage",
		},
		{
			name: "nested file",
			path: `D:\xue\code\storage\internal\handler\oss_bucket.go`,
			want: `internal\handler\oss_bucket.go`,
		},
		{
			name: "outside workspace",
			path: `D:\xue\code\external\oss_bucket.go`,
			want: "oss_bucket.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compactPathDisplayWithBase(tt.path, base); got != tt.want {
				t.Fatalf("compactPathDisplayWithBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolDisplayArgsHidesMetadataOnlyListArgs(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("LIST", map[string]any{"metadata": true}); got != "" {
		t.Fatalf("toolDisplayArgs(LIST metadata) = %q, want empty", got)
	}
}

func TestToolTitleDisplayArgsCompactsMutationPaths(t *testing.T) {
	t.Parallel()

	title := "Edit /home/xueyongzhi/WorkDir/code/caelis/internal/adapters/store/memory/store_test.go, /home/xueyongzhi/WorkDir/code/caelis/internal/adapters/store/sqlite/store_test.go"
	got := toolTitleDisplayArgs("PATCH", "edit", title)
	if want := "store_test.go, store_test.go"; got != want {
		t.Fatalf("toolTitleDisplayArgs() = %q, want %q", got, want)
	}
}

func TestToolDisplayResultHeaderCompactsWindowsReadPath(t *testing.T) {
	t.Parallel()

	base := `D:\xue\code\storage`
	header := `D:\xue\code\storage\internal\handler\oss_bucket.go 1~100`
	pathPart, rest, ok := splitLeadingPathHeader(header)
	if !ok {
		t.Fatalf("splitLeadingPathHeader() ok = false")
	}
	compact := compactPathDisplayWithBase(pathPart, base)
	if got := compact + rest; got != `internal\handler\oss_bucket.go 1~100` {
		t.Fatalf("compacted header = %q, want relative Windows header", got)
	}
}

func TestSplitLeadingPathHeaderHandlesTaggedPath(t *testing.T) {
	t.Parallel()

	pathPart, rest, ok := splitLeadingPathHeader(`<path>D:\repo\internal\foo.sql</path> 10~20`)
	if !ok {
		t.Fatalf("splitLeadingPathHeader() ok = false")
	}
	if pathPart != `D:\repo\internal\foo.sql` || rest != " 10~20" {
		t.Fatalf("splitLeadingPathHeader() = %q, %q, want tagged path and range rest", pathPart, rest)
	}
}

func TestToolDisplayResultHeaderPreservesSignedNonDiffLine(t *testing.T) {
	t.Parallel()

	output := "+1 for the win\nfallback"

	if got := toolDisplayResultHeader("PATCH", output); got != "+1 for the win" {
		t.Fatalf("toolDisplayResultHeader() = %q, want first non-diff signed line", got)
	}
}

func TestToolDisplayResultHeaderSkipsStandardDiffBody(t *testing.T) {
	t.Parallel()

	output := "-old line\n+new line"

	if got := toolDisplayResultHeader("PATCH", output); got != "" {
		t.Fatalf("toolDisplayResultHeader() = %q, want empty header for pure standard diff body", got)
	}
	if got := toolDisplayPanelOutput("PATCH", output); got != output {
		t.Fatalf("toolDisplayPanelOutput() = %q, want standard diff body preserved", got)
	}
}
