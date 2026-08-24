package tuiapp

import "testing"

func TestTaskDisplayUsesPublicHandleBeforeLegacyTaskID(t *testing.T) {
	t.Parallel()

	if got := taskControlDisplay(map[string]any{"action": "wait", "handle": "zuri", "task_id": "opaque-123"}); got != "Wait zuri" {
		t.Fatalf("taskControlDisplay() = %q, want public handle", got)
	}
	meta := map[string]any{"caelis": map[string]any{"runtime": map[string]any{
		"tool": map[string]any{"target_handle": "zuri", "target_id": "legacy"},
		"task": map[string]any{"handle": "zuri", "task_id": "opaque-123"},
	}}}
	output := toolDisplayMetaOutput("Task", meta)
	if output["handle"] != "zuri" || output["task_id"] != nil {
		t.Fatalf("toolDisplayMetaOutput() = %#v, want handle-only display identity", output)
	}
}

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

func TestToolDisplayArgsHidesMetadataOnlyGenericArgs(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("ExternalList", map[string]any{"metadata": true}); got != "" {
		t.Fatalf("toolDisplayArgs(ExternalList metadata) = %q, want empty", got)
	}
}

func TestViewImageDisplayUsesImagePath(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("ViewImage", map[string]any{"path": "/tmp/screens/pixel.png"}); got != "/tmp/screens/pixel.png" {
		t.Fatalf("toolDisplayArgs(ViewImage) = %q", got)
	}
	if got := toolTitleDisplayArgs("ViewImage", "read", "View /tmp/screens/pixel.png"); got != "/tmp/screens/pixel.png" {
		t.Fatalf("toolTitleDisplayArgs(ViewImage) = %q", got)
	}
	if got := approvalToolDisplayLabel("ViewImage"); got != "Viewed" {
		t.Fatalf("approvalToolDisplayLabel(ViewImage) = %q", got)
	}
}

func TestApprovalToolDisplayLabelUnifiesWriteAndPatchAsEdit(t *testing.T) {
	t.Parallel()

	if got := approvalToolDisplayLabel("Write"); got != "Edit" {
		t.Fatalf("approvalToolDisplayLabel(Write) = %q, want Edit", got)
	}
	if got := approvalToolDisplayLabel("Patch"); got != "Edit" {
		t.Fatalf("approvalToolDisplayLabel(Patch) = %q, want Edit", got)
	}
}

func TestToolDisplayArgsSkillUsesName(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("Skill", map[string]any{"name": "superpowers:brainstorming"}); got != "superpowers:brainstorming" {
		t.Fatalf("toolDisplayArgs(Skill) = %q, want skill name", got)
	}
}

func TestToolTitleDisplayArgsCompactsSkillContentWithoutChangingReadIdentity(t *testing.T) {
	t.Parallel()

	title := `Read <skill_content name="review">`
	if got := toolTitleDisplayArgs("", "read", title); got != "review" {
		t.Fatalf("toolTitleDisplayArgs(read skill_content) = %q, want review", got)
	}
	if got := toolDisplayArgs("Skill", map[string]any{"path": `<skill_content name="review">`}); got != "review" {
		t.Fatalf("toolDisplayArgs(Skill skill_content path) = %q, want review", got)
	}
}

func TestToolDisplayArgsExactSkillContentUsesToolPathAliases(t *testing.T) {
	t.Parallel()

	raw := map[string]any{"filePath": `<skill_content name="superpowers:brainstorm">`}
	if got := toolDisplayArgs("Skill", raw); got != "superpowers:brainstorm" {
		t.Fatalf("toolDisplayArgs(Skill raw only) = %q, want namespaced skill", got)
	}
}

func TestToolDisplayArgsGlobUsesProviderPatternAlias(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("Glob", map[string]any{"glob_pattern": "**/*.py"}); got != "**/*.py" {
		t.Fatalf("toolDisplayArgs(Glob glob_pattern) = %q, want pattern", got)
	}
}

func TestCompactExplorationArgsHaveNoOuterWrappers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		toolName string
		kind     string
		raw      map[string]any
		title    string
		want     string
	}{
		{name: "grok read raw path", kind: "read", raw: map[string]any{"target_file": "SKILL.md"}, want: "SKILL.md"},
		{name: "grok list raw path", kind: "read", raw: map[string]any{"target_directory": "docs"}, want: "docs"},
		{name: "grok list title fallback", kind: "read", title: "List `docs`", want: "docs"},
		{name: "read backtick title", kind: "read", title: "Read `SKILL.md`", want: "SKILL.md"},
		{name: "read quoted title", kind: "read", title: `Read "SKILL.md"`, want: "SKILL.md"},
		{name: "search backtick title", kind: "search", title: "Search `tool call`", want: "tool call"},
		{name: "search quoted title", kind: "search", title: `Search "tool call"`, want: "tool call"},
		{name: "grep structured query", toolName: "Grep", kind: "search", raw: map[string]any{"query": "tool call", "path": "internal"}, want: "tool call in internal"},
		{name: "grep slash query with path", toolName: "Grep", kind: "search", raw: map[string]any{"query": "foo/bar", "path": "internal"}, want: "foo/bar in internal"},
		{name: "grep structured quoted query", toolName: "Grep", kind: "search", raw: map[string]any{"query": `"needle"`}, want: `"needle"`},
		{name: "standard search structured backtick query", kind: "search", raw: map[string]any{"query": "`file`"}, want: "`file`"},
		{name: "web search structured query", toolName: "WebSearch", kind: "search", raw: map[string]any{"query": "tool call"}, want: "tool call"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallback := toolTitleDisplayArgs(tt.toolName, tt.kind, tt.title)
			if got := toolDisplayArgsForKind(tt.toolName, tt.kind, tt.raw, fallback); got != tt.want {
				t.Fatalf("toolDisplayArgsForKind() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := toolDisplayArgsForKind("ExternalTool", "other", map[string]any{"query": "tool call"}); got != `"tool call"` {
		t.Fatalf("generic non-exploration args = %q, want quoted compatibility display", got)
	}
}

func TestCompactExplorationTitleDetailRemovesOnlyFullOuterWrapper(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"`SKILL.md`":          "SKILL.md",
		`"C:\tmp\read"`:       `C:\tmp\read`,
		`"say \"hi\""`:        `say \"hi\"`,
		`"needle" 3 hits`:     `"needle" 3 hits`,
		"`missing.go` failed": "`missing.go` failed",
	}
	for input, want := range tests {
		if got := compactExplorationTitleDetail(input); got != want {
			t.Fatalf("compactExplorationTitleDetail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExplorationDetailPreservesStructuredArguments(t *testing.T) {
	t.Parallel()

	tests := []SubagentEvent{
		{Name: "Grep", ToolKind: "search", Args: `"needle"`},
		{Name: "Grep", ToolKind: "search", Args: `site:example.com/docs`},
		{Name: "Grep", ToolKind: "search", Args: `foo/bar in internal`},
		{Name: "Grep", ToolKind: "search", Args: `alpha,beta`},
		{Name: "Grep", ToolKind: "search", Args: `C:\tmp\read`},
		{Name: "Glob", ToolKind: "search", Args: "`*.go`"},
		{Name: "Read", ToolKind: "read", Args: `"C:\tmp\read"`},
	}
	for _, event := range tests {
		if got := explorationToolDetailForDisplay(event, "", explorationToolDetailSettled); got != event.Args {
			t.Fatalf("explorationToolDetailForDisplay(%q) = %q, want exact structured argument", event.Args, got)
		}
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
		{name: "glob", tool: "Glob", title: "Glob", want: ""},
		{name: "shell", tool: "RunCommand", kind: "execute", title: "Shell", want: ""},
		{name: "terminal", tool: "RunCommand", kind: "execute", title: "Terminal", want: ""},
		{name: "execute detail", tool: "RunCommand", kind: "execute", title: "Execute `pwd`", want: "`pwd`"},
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
	got := toolTitleDisplayArgs("Patch", "edit", title)
	if want := "store_test.go, store_test.go"; got != want {
		t.Fatalf("toolTitleDisplayArgs() = %q, want %q", got, want)
	}
}

func TestSearchDisplaySummaryOmitsFileCount(t *testing.T) {
	t.Parallel()

	if got := searchDisplaySummary(
		map[string]any{"pattern": "needle"},
		map[string]any{"count": 3, "file_count": 2},
	); got != `needle 3 hits` {
		t.Fatalf("searchDisplaySummary() = %q, want %q", got, `needle 3 hits`)
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
		{name: "absolute path", title: "Search /home/dev/WorkDir/private/storage", want: ""},
		{name: "windows absolute path", title: `Search D:\repo\storage`, want: ""},
		{name: "scoped relative query", title: "Search ./internal/foo Needle", want: `./internal/foo Needle`},
		{name: "scoped absolute query", title: "Search /tmp/foo Needle", want: `/tmp/foo Needle`},
		{name: "slash query", title: "Search foo/bar", want: `foo/bar`},
		{name: "relative path looking query", title: "Search internal/foo", want: `internal/foo`},
		{name: "web-style query", title: "Search site:example.com/docs", want: `site:example.com/docs`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTitleDisplayArgs("Grep", "search", tt.title); got != tt.want {
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

	if got := toolDisplayResultHeader("Patch", output); got != "+1 for the win" {
		t.Fatalf("toolDisplayResultHeader() = %q, want first non-diff signed line", got)
	}
}

func TestToolDisplayResultHeaderSkipsStandardDiffBody(t *testing.T) {
	t.Parallel()

	output := "-old line\n+new line"

	if got := toolDisplayResultHeader("Patch", output); got != "" {
		t.Fatalf("toolDisplayResultHeader() = %q, want empty header for pure standard diff body", got)
	}
	if got := toolDisplayPanelOutput("Patch", output); got != output {
		t.Fatalf("toolDisplayPanelOutput() = %q, want standard diff body preserved", got)
	}
}

func TestToolDisplayPanelOutputHidesSendMessageDispatchAck(t *testing.T) {
	t.Parallel()

	if got := toolDisplayPanelOutput("SendMessage", "Message sent."); got != "" {
		t.Fatalf("toolDisplayPanelOutput(SendMessage ack) = %q, want empty compact result", got)
	}
	if got := toolDisplayPanelOutput("SendMessage", "Message delivered."); got != "" {
		t.Fatalf("toolDisplayPanelOutput(legacy SendMessage ack) = %q, want replay-compatible compact result", got)
	}
	if got := toolDisplayPanelOutput("SendMessage", "Delivery outcome unknown; do not resend."); got != "Delivery outcome unknown; do not resend." {
		t.Fatalf("toolDisplayPanelOutput(SendMessage unknown) = %q, want unknown-outcome guidance", got)
	}
}
