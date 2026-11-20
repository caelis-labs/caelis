package gojaexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestBodyKeepsAllContentAndOriginalMedia(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Parts: []codemode.Part{{Kind: "text", Text: `{"value":1}`}, {Kind: "json", JSON: json.RawMessage(`{"value":1}`)}, {Kind: "text", Text: "second text"}, {Kind: "json", JSON: json.RawMessage(`{"value":2}`)}, {Kind: "media", Handle: "original-image", MimeType: "image/png"}}}, nil
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Read",{});if(r.data!==undefined)throw Error('inferred business data');return {text:r.text,image:r.media[0]};`), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Text string }
	if json.Unmarshal(out.Value, &got) != nil || got.Text != "{\"value\":1}\nsecond text" {
		t.Fatalf("content changed: %s", out.Value)
	}
	if len(out.Parts) != 1 || out.Parts[0].Handle != "original-image" {
		t.Fatalf("media identity lost: %+v", out.Parts)
	}
	out, err = (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Read",{});return {...r.media[0]};`), codemode.Limits{}, h)
	if err != nil || len(out.Parts) != 0 {
		t.Fatalf("copied media gained identity: %+v %v", out, err)
	}
}

type bodyEntryHost struct{ namedTestHost }

func (h *bodyEntryHost) Search(ctx context.Context, _ codemode.SearchRequest) (codemode.Reply, error) {
	return codemode.Reply{Value: json.RawMessage(`{"name":"Read"}`)}, nil
}

func TestBodyKeepsStructuredNull(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Value: json.RawMessage(`null`)}, nil
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const {data,text,media}=await tools.call("Optional",{});return {data,text,media};`), codemode.Limits{}, h)
	if err != nil || string(out.Value) != `{"data":null,"text":"","media":[]}` {
		t.Fatalf("expected structured null in Body: %s, %v", out.Value, err)
	}
}

func TestAllScriptEntriesReturnBody(t *testing.T) {
	h := &bodyEntryHost{namedTestHost: namedTestHost{names: []codemode.ToolRoute{{Name: "Read"}, {Name: "apps", Server: "dtw", Tool: "apps", Plugin: "desktop"}}}}
	calls := 0
	h.call = func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		calls++
		return codemode.Reply{Value: json.RawMessage(`{"value":42}`), Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{"value":42}`)}, {Kind: "media", Handle: "original"}}}, nil
	}
	code := `const results=[await Read({}),await dtw.apps({}),await tools.native.Read({}),await tools.mcp.dtw.apps({}),await tools.plugins.desktop.dtw.apps({}),await tools.call("Read",{})];for(const r of results){if(r.text!==""||r.data!==r.data||r.data.value!==42||r.media.length!==1||r.json!==undefined)throw Error("entry has a different result contract");}const entry=await tools.search({name:"Read"});if(entry.name!=="Read"||entry.data!==undefined)throw Error("search should return a plain entry");return results.length;`
	out, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || string(out.Value) != "6" || calls != 6 {
		t.Fatalf("Body entries output=%s calls=%d error=%v", out.Value, calls, err)
	}
}
