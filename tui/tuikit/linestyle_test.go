package tuikit

import (
	"strings"
	"testing"
)

func TestDetectLineStyle(t *testing.T) {
	tests := []struct {
		line string
		want LineStyle
	}{
		{"· Hello world", LineStyleAssistant},
		{"* Hello world", LineStyleAssistant},
		{"› thinking about it", LineStyleReasoning},
		{"│ thinking about it", LineStyleReasoning},
		{"▌ user input", LineStyleUser},
		{"> user input", LineStyleUser},
		{"▸ tool_call {}", LineStyleTool},
		{"▾ tool_call {expanded}", LineStyleTool},
		{"✓ tool_result ok", LineStyleTool},
		{"✗ tool_result failed", LineStyleTool},
		{"? Approval: run", LineStyleTool},
		{"error: something broke", LineStyleError},
		{"warn: be careful", LineStyleWarn},
		{"! llm request failed, retrying in 2s", LineStyleWarn},
		{"note: fyi", LineStyleNote},
		{"  +added line", LineStyleDiffAdd},
		{"  -removed line", LineStyleDiffRemove},
		{"  --- old/file.go", LineStyleDiffHeader},
		{"  +++ new/file.go", LineStyleDiffHeader},
		{"  @@ -5,1 +5,1 @@", LineStyleDiffHunk},
		{"  key  value", LineStyleKeyValue},
		{"Section Title", LineStyleSection},
		{"", LineStyleDefault},
		{"   ", LineStyleDefault},
		{"some: mixed text", LineStyleDefault},
	}
	for _, tt := range tests {
		got := DetectLineStyle(tt.line)
		if got != tt.want {
			t.Errorf("DetectLineStyle(%q) = %d, want %d", tt.line, got, tt.want)
		}
	}
}

func TestColorizeLogLine(t *testing.T) {
	theme := DefaultTheme()
	// Just verify the function doesn't panic and returns non-empty for each style.
	for style := LineStyleDefault; style <= LineStyleDiffHunk; style++ {
		line := "test line"
		result := ColorizeLogLine(line, style, theme)
		if result == "" {
			t.Errorf("ColorizeLogLine returned empty for style %d", style)
		}
	}
}

func TestColorizeAssistantPrefix(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("· hello", LineStyleAssistant, theme)
	// In non-TTY (CI) environments lipgloss may strip colors; just ensure
	// the textual content is preserved.
	if result == "" {
		t.Error("expected non-empty output")
	}
	if len(result) < len("· hello") {
		t.Errorf("expected at least original length, got %d", len(result))
	}
}

func TestColorizeToolCall(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("▸ read_file {path: /foo}", LineStyleTool, theme)
	if result == "" {
		t.Error("expected non-empty colored tool call output")
	}
}

func TestColorizeToolResult(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("✓ read_file success", LineStyleTool, theme)
	if result == "" {
		t.Error("expected non-empty colored tool result output")
	}
}

func TestColorizeToolFailure(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("✗ BASH exit 1", LineStyleTool, theme)
	if result == "" {
		t.Fatal("expected non-empty colored tool failure output")
	}
	if !strings.Contains(result, "BASH") || !strings.Contains(result, "exit 1") {
		t.Fatalf("expected tool failure text preserved, got %q", result)
	}
}

func TestColorizeUserLine_PreservesMentionToken(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("▌ please check @deploy/build.sh, now", LineStyleUser, theme)
	if result == "" {
		t.Fatal("expected non-empty user line")
	}
	if !strings.Contains(result, "@deploy/build.sh") {
		t.Fatalf("expected mention token preserved, got %q", result)
	}
}

func TestColorizeDiffLines(t *testing.T) {
	theme := DefaultTheme()
	add := ColorizeLogLine("  +new code", LineStyleDiffAdd, theme)
	if add == "" {
		t.Error("expected non-empty diff add")
	}
	remove := ColorizeLogLine("  -old code", LineStyleDiffRemove, theme)
	if remove == "" {
		t.Error("expected non-empty diff remove")
	}
}

func TestColorizeWarnLine_UsesSymbolPrefix(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("warn: be careful", LineStyleWarn, theme)
	if !strings.Contains(result, "! ") {
		t.Fatalf("expected symbol prefix in warn line, got %q", result)
	}
	if !strings.Contains(result, "be careful") {
		t.Fatalf("expected warn message preserved, got %q", result)
	}
}

func TestCountLeadingSpaces(t *testing.T) {
	if countLeadingSpaces("  hello") != 2 {
		t.Error("expected 2")
	}
	if countLeadingSpaces("\thello") != 4 {
		t.Error("expected 4 for tab")
	}
	if countLeadingSpaces("hello") != 0 {
		t.Error("expected 0")
	}
}

// ---------------------------------------------------------------------------
// Block continuation tests (DetectLineStyleWithContext)
// ---------------------------------------------------------------------------

func TestBlockContinuationFromReasoning(t *testing.T) {
	// First line detected as reasoning.
	first := DetectLineStyleWithContext("› thinking about it", LineStyleDefault)
	if first != LineStyleReasoning {
		t.Fatalf("expected reasoning, got %d", first)
	}
	// Continuation line with no prefix should inherit reasoning.
	cont := DetectLineStyleWithContext("still thinking here", LineStyleReasoning)
	if cont != LineStyleReasoning {
		t.Fatalf("expected reasoning continuation, got %d", cont)
	}
}

func TestBlockContinuationFromAssistant(t *testing.T) {
	first := DetectLineStyleWithContext("· hello world", LineStyleDefault)
	if first != LineStyleAssistant {
		t.Fatalf("expected assistant, got %d", first)
	}
	cont := DetectLineStyleWithContext("more text from assistant", LineStyleAssistant)
	if cont != LineStyleAssistant {
		t.Fatalf("expected assistant continuation, got %d", cont)
	}
}

func TestBlockContinuationFromTool(t *testing.T) {
	first := DetectLineStyleWithContext("▸ read_file {}", LineStyleDefault)
	if first != LineStyleTool {
		t.Fatalf("expected tool, got %d", first)
	}
	cont := DetectLineStyleWithContext("  some tool output", LineStyleTool)
	if cont != LineStyleTool {
		t.Fatalf("expected tool continuation, got %d", cont)
	}
}

func TestBlockContinuationBreaksOnNewPrefix(t *testing.T) {
	// Reasoning followed by explicit assistant line.
	next := DetectLineStyleWithContext("* new assistant response", LineStyleReasoning)
	if next != LineStyleAssistant {
		t.Fatalf("expected new prefix to override continuation, got %d", next)
	}
}

func TestBlockContinuationNotFromUser(t *testing.T) {
	// User style should NOT be continuable.
	cont := DetectLineStyleWithContext("plain text", LineStyleUser)
	if cont == LineStyleUser {
		t.Fatal("expected plain text after user NOT to inherit user style")
	}
}

func TestBlockContinuationEmptyLineResets(t *testing.T) {
	// Empty line should always be default, not inherit.
	cont := DetectLineStyleWithContext("", LineStyleReasoning)
	if cont != LineStyleDefault {
		t.Fatalf("expected default for empty line, got %d", cont)
	}
}

func TestIsBlockContinuable(t *testing.T) {
	if !isBlockContinuable(LineStyleAssistant) {
		t.Error("assistant should be continuable")
	}
	if !isBlockContinuable(LineStyleReasoning) {
		t.Error("reasoning should be continuable")
	}
	if !isBlockContinuable(LineStyleTool) {
		t.Error("tool should be continuable")
	}
	if isBlockContinuable(LineStyleUser) {
		t.Error("user should NOT be continuable")
	}
	if isBlockContinuable(LineStyleDefault) {
		t.Error("default should NOT be continuable")
	}
}

func TestIsRetryLine(t *testing.T) {
	if !IsRetryLine("! llm request failed, retrying in 2s (1/5): model: http status 400") {
		t.Error("expected retry detection for bang-prefix retry line")
	}
	if !IsRetryLine("warn: retrying request after failure") {
		t.Error("expected retry detection for warn-prefix retry line")
	}
	if IsRetryLine("! some other warning message") {
		t.Error("non-retry bang line should not be detected as retry")
	}
	if IsRetryLine("* assistant text about retrying") {
		t.Error("assistant text should not match retry detection")
	}
	if IsRetryLine("normal text") {
		t.Error("normal text should not match retry detection")
	}
}

func TestIsLogLine(t *testing.T) {
	if !IsLogLine(LineStyleTool) {
		t.Error("tool should be a log line")
	}
	if !IsLogLine(LineStyleWarn) {
		t.Error("warn should be a log line")
	}
	if !IsLogLine(LineStyleError) {
		t.Error("error should be a log line")
	}
	if !IsLogLine(LineStyleNote) {
		t.Error("note should be a log line")
	}
	if IsLogLine(LineStyleAssistant) {
		t.Error("assistant should not be a log line")
	}
	if IsLogLine(LineStyleUser) {
		t.Error("user should not be a log line")
	}
	if IsLogLine(LineStyleDefault) {
		t.Error("default should not be a log line")
	}
}

func TestColorizeBangPrefixLine(t *testing.T) {
	theme := DefaultTheme()
	result := ColorizeLogLine("! some warning with bang", LineStyleWarn, theme)
	if !strings.Contains(result, "! ") {
		t.Fatalf("expected bang prefix in output, got %q", result)
	}
	if !strings.Contains(result, "some warning with bang") {
		t.Fatalf("expected message preserved, got %q", result)
	}
}

func TestLineExtraGutter(t *testing.T) {
	tests := []struct {
		style LineStyle
		want  int
	}{
		{LineStyleAssistant, 0},
		{LineStyleReasoning, 0},
		{LineStyleDefault, 0},
		{LineStyleUser, GutterUser - GutterNarrative},
		{LineStyleTool, GutterLog - GutterNarrative},
		{LineStyleWarn, GutterLog - GutterNarrative},
		{LineStyleError, GutterLog - GutterNarrative},
		{LineStyleNote, GutterLog - GutterNarrative},
	}
	for _, tt := range tests {
		got := LineExtraGutter(tt.style)
		if len(got) != tt.want {
			t.Errorf("LineExtraGutter(%v) = %d spaces, want %d", tt.style, len(got), tt.want)
		}
	}
}

func TestSpacingTokenValues(t *testing.T) {
	if SpaceTight != 0 {
		t.Errorf("SpaceTight = %d, want 0", SpaceTight)
	}
	if SpaceNormal != 1 {
		t.Errorf("SpaceNormal = %d, want 1", SpaceNormal)
	}
	if SpaceBlock != 2 {
		t.Errorf("SpaceBlock = %d, want 2", SpaceBlock)
	}
}
