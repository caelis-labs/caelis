package gojaexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestBusinessResultMutationSelectsActualReferences(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{CallID: "original", MCP: true, Parts: []codemode.Part{{Kind: "text", Text: "original text"}, {Kind: "media", Handle: "owned-image", MimeType: "image/png"}}}, nil
	}}
	for _, tc := range []struct {
		name, code            string
		media, text, original bool
	}{
		{"unchanged", `return r;`, false, false, true},
		{"nested_unchanged", `return {record:r};`, true, true, false},
		{"delete", `delete r.content;return r;`, false, false, false},
		{"nested_delete", `delete r.content;return {record:r};`, false, false, false},
		{"copy_delete", `delete r.content;return {...r};`, false, false, false},
		{"copy_references", `return {...r};`, true, false, false},
		{"copied_handle", `r.content=[r.content[0],{...r.content[1]}];return r;`, false, false, false},
		{"nested_copied_handle", `r.content=[r.content[0],{...r.content[1]}];return {record:r};`, false, false, false},
		{"select_original", `const image=r.content[1];delete r.content;return {record:r,image};`, true, false, false},
		{"getter_once", `const content=r.content;let n=0;Object.defineProperty(r,'content',{enumerable:true,get(){if(n++)throw Error('read twice');return content;}});return r;`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Target",{});`+tc.code), codemode.Limits{}, h)
			if err != nil {
				t.Fatal(err)
			}
			var media, text bool
			for _, p := range out.Parts {
				media = media || p.Kind == "media" && p.Handle == "owned-image"
				text = text || p.Kind == "text" && p.Text == "original text"
			}
			if media != tc.media || text != tc.text || (out.CallID == "original") != tc.original {
				t.Fatalf("unexpected projection: %+v", out)
			}
			if strings.Contains(tc.name, "delete") && strings.Contains(string(out.Value), "content") {
				t.Fatalf("deleted content survived: %s", out.Value)
			}
		})
	}
}
