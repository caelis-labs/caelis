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

func TestToolDisplayArgsSkillUsesName(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("SKILL", map[string]any{"name": "superpowers:brainstorming"}); got != "superpowers:brainstorming" {
		t.Fatalf("toolDisplayArgs(SKILL) = %q, want skill name", got)
	}
}

func TestToolDisplayArgsSkillContentReadUsesSkillName(t *testing.T) {
	t.Parallel()

	title := `Read <skill_content name="review">`
	if got := toolDisplaySemanticOverride("READ", "read", title, nil); got != "SKILL" {
		t.Fatalf("toolDisplaySemanticOverride() = %q, want SKILL", got)
	}
	if got := toolTitleDisplayArgs("SKILL", "read", title); got != "review" {
		t.Fatalf("toolTitleDisplayArgs(SKILL skill_content) = %q, want review", got)
	}
	if got := toolDisplayArgs("SKILL", map[string]any{"path": `<skill_content name="review">`}); got != "review" {
		t.Fatalf("toolDisplayArgs(SKILL skill_content path) = %q, want review", got)
	}
}

func TestToolDisplayArgsSkillContentRawInputOnlyUsesToolPathAliases(t *testing.T) {
	t.Parallel()

	raw := map[string]any{"filePath": `<skill_content name="superpowers:brainstorm">`}
	if got := toolDisplaySemanticOverride("READ", "read", "", raw); got != "SKILL" {
		t.Fatalf("toolDisplaySemanticOverride(raw only) = %q, want SKILL", got)
	}
	if got := toolDisplayArgs("SKILL", raw); got != "superpowers:brainstorm" {
		t.Fatalf("toolDisplayArgs(SKILL raw only) = %q, want namespaced skill", got)
	}
}

func TestToolDisplaySemanticOverrideDoesNotTreatOrdinaryReadAsSkill(t *testing.T) {
	t.Parallel()

	raw := map[string]any{"path": "src/foo.go"}
	if got := toolDisplaySemanticOverride("READ", "read", "Read src/foo.go", raw); got != "" {
		t.Fatalf("toolDisplaySemanticOverride(ordinary read) = %q, want empty", got)
	}
}

func TestToolDisplayArgsGlobUsesProviderPatternAlias(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("GLOB", map[string]any{"glob_pattern": "**/*.py"}); got != "**/*.py" {
		t.Fatalf("toolDisplayArgs(GLOB glob_pattern) = %q, want pattern", got)
	}
}

func TestToolTitleDisplayArgsSuppressesGenericProviderTitles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		kind  string
		title string
		want  string
	}{
		{name: "glob", tool: "GLOB", title: "Glob", want: ""},
		{name: "shell", tool: "RUN_COMMAND", kind: "execute", title: "Shell", want: ""},
		{name: "terminal", tool: "RUN_COMMAND", kind: "execute", title: "Terminal", want: ""},
		{name: "execute detail", tool: "RUN_COMMAND", kind: "execute", title: "Execute `pwd`", want: "`pwd`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTitleDisplayArgs(tt.tool, tt.kind, tt.title); got != tt.want {
				t.Fatalf("toolTitleDisplayArgs() = %q, want %q", got, tt.want)
			}
		})
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

func TestToolTitleDisplayArgsSearchPathScopesAndSlashQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{name: "current directory", title: "Search .", want: ""},
		{name: "parent directory", title: "Search ..", want: ""},
		{name: "explicit relative directory", title: "Search ./internal/foo", want: ""},
		{name: "explicit parent directory", title: "Search ../storage", want: ""},
		{name: "windows explicit relative directory", title: `Search .\internal\foo`, want: ""},
		{name: "absolute path", title: "Search /home/xueyongzhi/WorkDir/ctstackcmp/storage", want: ""},
		{name: "windows absolute path", title: `Search D:\repo\storage`, want: ""},
		{name: "scoped relative query", title: "Search ./internal/foo Needle", want: `"./internal/foo Needle"`},
		{name: "scoped absolute query", title: "Search /tmp/foo Needle", want: `"/tmp/foo Needle"`},
		{name: "slash query", title: "Search foo/bar", want: `"foo/bar"`},
		{name: "relative path looking query", title: "Search internal/foo", want: `"internal/foo"`},
		{name: "web-style query", title: "Search site:example.com/docs", want: `"site:example.com/docs"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTitleDisplayArgs("SEARCH", "search", tt.title); got != tt.want {
				t.Fatalf("toolTitleDisplayArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSearchTitleDetailIsPathOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		detail string
		want   bool
	}{
		{detail: ".", want: true},
		{detail: "..", want: true},
		{detail: "./internal/foo", want: true},
		{detail: "../storage", want: true},
		{detail: `.\internal\foo`, want: true},
		{detail: "/abs/path", want: true},
		{detail: `D:\repo\storage`, want: true},
		{detail: "./internal/foo Needle", want: false},
		{detail: "/tmp/foo Needle", want: false},
		{detail: "foo/bar", want: false},
		{detail: "internal/foo", want: false},
		{detail: "site:example.com/docs", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.detail, func(t *testing.T) {
			if got := searchTitleDetailIsPathOnly(tt.detail); got != tt.want {
				t.Fatalf("searchTitleDetailIsPathOnly() = %v, want %v", got, tt.want)
			}
		})
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
