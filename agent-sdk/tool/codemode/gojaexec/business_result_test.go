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
		return codemode.Reply{CallID: "original", Value: json.RawMessage(`{"content":"business"}`), Parts: []codemode.Part{{Kind: "text", Text: "original text"}, {Kind: "media", Handle: "owned-image", MimeType: "image/png"}}}, nil
	}}
	for _, tc := range []struct {
		name, code            string
		media, text, original bool
	}{
		{"unchanged", `return r;`, false, false, true},
		{"nested_unchanged", `return {record:r};`, true, false, false},
		{"delete", `const value=r.data;value.content=r.media;delete value.content;return value;`, false, false, false},
		{"nested_delete", `const value=r.data;value.content=r.media;delete value.content;return {record:value};`, false, false, false},
		{"copy_delete", `const value=r.data;value.content=r.media;delete value.content;return {...value};`, false, false, false},
		{"copy_references", `return {...r};`, true, false, false},
		{"copied_handle", `return {media:r.media.map(p=>({...p}))};`, false, false, false},
		{"nested_copied_handle", `return {record:{media:r.media.map(p=>({...p}))}};`, false, false, false},
		{"select_original", `const value=r.data;delete value.content;return {record:value,image:r.media[0]};`, true, false, false},
		{"getter_once", `let n=0;return {get selected(){if(n++)throw Error('read twice');return r.media[0];}};`, true, false, false},
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
