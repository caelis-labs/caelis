package acputil

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
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

func TestStripTerminalConsoleFenceOutputValue(t *testing.T) {
	t.Parallel()

	fenced := "```console\nline\n```\n"
	cases := []struct {
		name  string
		in    any
		check func(t *testing.T, got any)
	}{
		{
			name: "map output keys",
			in: map[string]any{
				"stdout": fenced,
				"id":     fenced,
			},
			check: func(t *testing.T, got any) {
				t.Helper()
				out, _ := got.(map[string]any)
				if out["stdout"] != "line\n" {
					t.Fatalf("stdout = %#v, want stripped output", out["stdout"])
				}
				if out["id"] != fenced {
					t.Fatalf("id = %#v, want unrelated key preserved", out["id"])
				}
			},
		},
		{
			name: "text content map",
			in:   map[string]any{"type": "text", "text": fenced},
			check: func(t *testing.T, got any) {
				t.Helper()
				out, _ := got.(map[string]any)
				if out["text"] != "line\n" {
					t.Fatalf("text = %#v, want stripped text content", out["text"])
				}
			},
		},
		{
			name: "json raw message",
			in:   json.RawMessage(`{"type":"text","text":"` + "```console\\nline\\n```\\n" + `"}`),
			check: func(t *testing.T, got any) {
				t.Helper()
				out, _ := got.(map[string]any)
				if out["text"] != "line\n" {
					t.Fatalf("text = %#v, want stripped raw text content", out["text"])
				}
			},
		},
		{
			name: "slice",
			in:   []any{map[string]any{"stderr": fenced}, fenced},
			check: func(t *testing.T, got any) {
				t.Helper()
				out, _ := got.([]any)
				if out[0].(map[string]any)["stderr"] != "line\n" || out[1] != "line\n" {
					t.Fatalf("slice = %#v, want stripped terminal output values", out)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t, StripTerminalConsoleFenceOutputValue(tc.in))
		})
	}
}

func TestStripTerminalConsoleFenceMeta(t *testing.T) {
	t.Parallel()

	fenced := "```console\nline\n```\n"
	meta := map[string]any{
		"terminal_output": map[string]any{
			"terminal_id": "call-1",
			"data":        fenced,
		},
		"vendor": map[string]any{"trace": fenced},
	}
	got := StripTerminalConsoleFenceMeta(meta)

	output, _ := got["terminal_output"].(map[string]any)
	if output["data"] != "line\n" {
		t.Fatalf("terminal_output data = %#v, want stripped output", output["data"])
	}
	if output["terminal_id"] != "call-1" {
		t.Fatalf("terminal_id = %#v, want preserved id", output["terminal_id"])
	}
	if meta["terminal_output"].(map[string]any)["data"] != fenced {
		t.Fatalf("original meta was mutated: %#v", meta)
	}
	if got["vendor"].(map[string]any)["trace"] != fenced {
		t.Fatalf("vendor trace = %#v, want unrelated meta preserved", got["vendor"])
	}
}

func TestStripTerminalConsoleFenceToolCallUpdate(t *testing.T) {
	t.Parallel()

	fenced := "```console\nline\n```\n"
	title := fenced
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
		Meta: map[string]any{
			"terminal_output": map[string]any{"data": fenced},
		},
	})

	if got.Title == nil || *got.Title != fenced {
		t.Fatalf("title = %#v, want non-terminal title preserved", got.Title)
	}
	if got.RawOutput.(map[string]any)["stdout"] != "line\n" {
		t.Fatalf("raw output = %#v, want stripped stdout", got.RawOutput)
	}
	if text := acpschema.ExtractTextValue(got.Content[0].Content); text != "line\n" {
		t.Fatalf("terminal content = %q, want stripped text", text)
	}
	if text := acpschema.ExtractTextValue(got.Content[1].Content); text != fenced {
		t.Fatalf("non-terminal content = %q, want original text", text)
	}
	output, _ := got.Meta["terminal_output"].(map[string]any)
	if output["data"] != "line\n" {
		t.Fatalf("terminal meta = %#v, want stripped data", got.Meta)
	}
}
