package gojaexec

import (
	"context"
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"strings"
	"testing"
)

func TestBodyKeepsAllContentAndOriginalMedia(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Value: json.RawMessage(`null`)}, nil
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const {data,text,media}=await tools.call("Optional",{});return {data,text,media};`), codemode.Limits{}, h)
	if err != nil || string(out.Value) != `{"data":null,"text":"","media":[]}` {
		t.Fatalf("expected structured null in Body: %s, %v", out.Value, err)
	}
}

func TestAllScriptEntriesReturnBody(t *testing.T) {
	t.Parallel()
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

func TestReviewS1AuthenticatedReplyGetterEvaluatedOnce(t *testing.T) {
	t.Parallel()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "media", Handle: "owned-A", MimeType: "image/png"}}}, nil
	}}
	for _, tc := range []struct{ name, code string }{
		{"reply_parts_getter", `const r=await tools.call("Target",{});const real=r.media;const selected={};let n=0;Object.defineProperty(selected,"parts",{enumerable:true,get(){return ++n===1?[{kind:"json",json:{reads:n}}]:real;}});return selected;`},
		{"reply_extra_property", `const r=await tools.call("Target",{});const selected={note:"ordinary data"};let n=0;Object.defineProperty(selected,"parts",{enumerable:true,get(){return [{kind:"json",json:{reads:++n}}];}});return selected;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := (&Executor{}).Execute(t.Context(), fixed(tc.code), codemode.Limits{}, h)
			if err != nil {
				t.Fatal(err)
			}
			var value struct {
				Parts []struct {
					JSON struct {
						Reads int `json:"reads"`
					} `json:"json"`
				} `json:"parts"`
			}
			_ = json.Unmarshal(output.Value, &value)
			if len(output.Parts) != 0 || len(value.Parts) != 1 || value.Parts[0].JSON.Reads != 1 {
				t.Fatalf("getter preflight changed returned value/media: value=%s selected=%+v", output.Value, output.Parts)
			}
		})
	}
}

// Selection uses the actual single budgeted element read, including accessors.
func TestReplyPartElementAccessorEvaluatedOnce(t *testing.T) {
	t.Parallel()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "media", Handle: "owned-A"}}}, nil
	}}
	code := `const r=await tools.call("Target",{});const real=r.media[0];const selected={parts:[real]};let n=0;Object.defineProperty(selected.parts,"0",{enumerable:true,get(){return ++n===1?{kind:"json",json:{reads:n}}:real}});return selected;`
	output, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Parts []struct {
			JSON struct {
				Reads int `json:"reads"`
			} `json:"json"`
		} `json:"parts"`
	}
	if json.Unmarshal(output.Value, &got) != nil || len(got.Parts) != 1 || got.Parts[0].JSON.Reads != 1 || len(output.Parts) != 0 {
		t.Fatalf("element getter changed value/media: %+v", output)
	}
}

func TestNestedAuthenticatedMediaKeepsEncodingBudgets(t *testing.T) {
	t.Parallel()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Value: json.RawMessage(`{}`), Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{}`)}, {Kind: "media", Handle: "owned-A"}, {Kind: "media", Handle: "owned-B"}}}, nil
	}}
	for _, tc := range []struct{ code, want string }{
		{`const value=r.data;value.selected=r.media;return value;`, "returned media budget exceeded"},
		{`const value=r.data;value.self=value;return value;`, "cyclic JSON"},
		{`let v=r.media[0];for(let i=0;i<130;i++)v={nested:v};const value=r.data;value.selected=v;return value;`, "JSON depth exceeded"},
	} {
		_, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Target",{});`+tc.code), codemode.Limits{Calls: 1}, h)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("got %v; want %s", err, tc.want)
		}
	}
}

func TestBusinessResultMutationSelectsActualReferences(t *testing.T) {
	t.Parallel()
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
