package gojaexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestUnpackKeepsAllContentAndOriginalMedia(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Parts: []codemode.Part{
			{Kind: "text", Text: `{"value":1}`},
			{Kind: "json", JSON: json.RawMessage(`{"value":1}`)},
			{Kind: "text", Text: "second text"},
			{Kind: "json", JSON: json.RawMessage(`{"value":2}`)},
			{Kind: "media", Handle: "original-image", MimeType: "image/png"},
		}}, nil
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Read",{});const u=tools.unpack(r);return {json:u.json,text:u.text,image:u.media[0]};`), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		JSON []struct{ Value int } `json:"json"`
		Text []string              `json:"text"`
	}
	if err = json.Unmarshal(out.Value, &got); err != nil || len(got.JSON) != 2 || got.JSON[0].Value != 1 || got.JSON[1].Value != 2 || len(got.Text) != 2 || got.Text[0] != `{"value":1}` || got.Text[1] != "second text" {
		t.Fatalf("content changed: %s (%v)", out.Value, err)
	}
	if len(out.Parts) != 1 || out.Parts[0].Handle != "original-image" {
		t.Fatalf("media identity lost: %+v", out.Parts)
	}
	out, err = (&Executor{}).Execute(t.Context(), fixed(`const u=tools.unpack(await tools.call("Read",{}));return {...u.media[0]};`), codemode.Limits{}, h)
	if err != nil || len(out.Parts) != 0 {
		t.Fatalf("copied media gained identity: %+v (%v)", out, err)
	}
	_, err = (&Executor{}).Execute(t.Context(), fixed(`return tools.unpack({parts:[]});`), codemode.Limits{}, h)
	if err == nil || !strings.Contains(err.Error(), "original tool reply") {
		t.Fatalf("accepted fabricated reply: %v", err)
	}
}
