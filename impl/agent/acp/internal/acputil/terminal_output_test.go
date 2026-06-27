package acputil

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestStripTerminalConsoleFenceText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "whole console fence",
			in:   "```console\ndiff --git a/file b/file\n```\n",
			want: "diff --git a/file b/file\n",
		},
		{
			name: "crlf console fence",
			in:   "```console\r\nline\r\n```\r\n",
			want: "line\r\n",
		},
		{
			name: "non console fence stays literal",
			in:   "```json\n{\"ok\":true}\n```",
			want: "```json\n{\"ok\":true}\n```",
		},
		{
			name: "embedded fence stays literal",
			in:   "before\n```console\nline\n```\nafter",
			want: "before\n```console\nline\n```\nafter",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StripTerminalConsoleFenceText(tc.in); got != tc.want {
				t.Fatalf("StripTerminalConsoleFenceText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripTerminalConsoleFenceToolCallUpdate(t *testing.T) {
	t.Parallel()

	fenced := "```console\nline\n```\n"
	title := fenced
	meta := metautil.WithRuntimeSection(nil, metautil.Terminal, map[string]any{"data": fenced})
	got := StripTerminalConsoleFenceToolCallUpdate(client.ToolCallUpdate{
		Title:     &title,
		RawOutput: map[string]any{"stdout": fenced},
		Content: []client.ToolCallContent{{
			Type:    "terminal",
			Content: acpschema.TextContent{Type: "text", Text: fenced},
		}, {
			Type:    "content",
			Content: acpschema.TextContent{Type: "text", Text: fenced},
		}},
		Meta: meta,
	})

	if got.Title == nil || *got.Title != fenced {
		t.Fatalf("title = %#v, want non-terminal title preserved", got.Title)
	}
	if got.RawOutput.(map[string]any)["stdout"] != fenced {
		t.Fatalf("raw output = %#v, want original stdout", got.RawOutput)
	}
	if text := acpschema.ExtractTextValue(got.Content[0].Content); text != "line\n" {
		t.Fatalf("terminal content = %q, want stripped text", text)
	}
	if text := acpschema.ExtractTextValue(got.Content[1].Content); text != fenced {
		t.Fatalf("non-terminal content = %q, want original text", text)
	}
	if output := metautil.RuntimeSection(got.Meta, metautil.Terminal); output["data"] != fenced {
		t.Fatalf("terminal meta = %#v, want original data", got.Meta)
	}
}

func TestStripTerminalConsoleFenceToolCallUpdateStripsExecuteContent(t *testing.T) {
	t.Parallel()

	fenced := "```console\nclean\n```\n"
	kind := acpschema.ToolKindExecute
	got := StripTerminalConsoleFenceToolCallUpdate(client.ToolCallUpdate{
		Kind: &kind,
		Content: []client.ToolCallContent{{
			Type:    "content",
			Content: acpschema.TextContent{Type: "text", Text: fenced},
		}},
	})

	if text := acpschema.ExtractTextValue(got.Content[0].Content); text != "clean\n" {
		t.Fatalf("execute content = %q, want stripped console output", text)
	}
}

func TestStripTerminalConsoleFenceToolCallUpdateStripsClaudeBashContent(t *testing.T) {
	t.Parallel()

	fenced := "```console\nFri Jun 26 14:35:27 CST 2026\n```\n"
	want := "Fri Jun 26 14:35:27 CST 2026\n"
	got := StripTerminalConsoleFenceToolCallUpdate(client.ToolCallUpdate{
		RawOutput: "Fri Jun 26 14:35:27 CST 2026",
		Content: []client.ToolCallContent{{
			Type:    "content",
			Content: acpschema.TextContent{Type: "text", Text: fenced},
		}},
		Meta: map[string]any{
			"claudeCode": map[string]any{
				"toolName": "Bash",
			},
		},
	})

	if text := acpschema.ExtractTextValue(got.Content[0].Content); text != want {
		t.Fatalf("claude bash content = %q, want stripped console output", text)
	}
}

func TestStripTerminalConsoleFenceToolCallUpdateStripsDecodedTextMapContent(t *testing.T) {
	t.Parallel()

	fenced := "```console\nhello\n```\n"
	kind := acpschema.ToolKindExecute
	got := StripTerminalConsoleFenceToolCallUpdate(client.ToolCallUpdate{
		Kind: &kind,
		Content: []client.ToolCallContent{{
			Type:    "content",
			Content: map[string]any{"type": "text", "text": fenced},
		}},
	})

	content, _ := got.Content[0].Content.(map[string]any)
	if content["text"] != "hello\n" {
		t.Fatalf("decoded text content = %#v, want stripped console output", content)
	}
}
