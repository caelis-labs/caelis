package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestJSExecReturnOnlySelectedMedia(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		want       []string
		failed     bool
		wantJSON   string
	}{
		{"direct", `return await tools.call("PictureA",{});`, []string{"A"}, false, ""},
		{"unreturned", `await tools.call("PictureA",{});return {ok:true};`, nil, false, ""},
		{"other_call", `await tools.call("PictureA",{});return await tools.call("PictureB",{});`, []string{"B"}, false, ""},
		{"foreign", `const r=await tools.call("PictureA",{});r.parts[2].handle="another-invocation";return r;`, nil, true, ""},
		{"plain_foreign_data", `return {parts:[{kind:"media",handle:"another-invocation"}]};`, nil, false, ""},
		{"copied_fields", `const r=await tools.call("PictureA",{});return {parts:[{...r.parts[2]}]};`, nil, false, ""},
		{"combined", `const a=await tools.call("PictureA",{});const b=await tools.call("PictureB",{});return {selected:b.parts[2],record:{kind:"media",handle:"ordinary-data"}};`, []string{"B"}, false, ""},
		{"selected_reply_parts", `const r=await tools.call("PictureA",{});r.parts=r.parts.filter(p=>p.kind!=="json");return r;`, []string{"A"}, false, ""},
		{"getter_once", `const r=await tools.call("PictureA",{});let n=0;return {get selected(){return n++===0?{kind:"media",handle:"ordinary-data"}:r.parts[2]}};`, nil, false, ""},
		{"proxy_array_reads", `const r=await tools.call("PictureA",{});r.parts=new Proxy([r.parts[2]],{get(t,k){if(k==="0")return {kind:"json",json:{visible:"actual proxy read"}};return Reflect.get(t,k)}});return r;`, nil, false, "actual proxy read"},
		{"text_business_field", `const r=await tools.call("PictureA",{});r.parts=[r.parts[0]];r.parts[0].json={note:"text business"};return r;`, nil, false, "text business"},
		{"json_business_field", `const r=await tools.call("PictureA",{});r.parts=[r.parts[1]];r.parts[0].text="json business";return r;`, nil, false, "json business"},
		{"json_missing_payload", `const r=await tools.call("PictureA",{});r.parts=[r.parts[1]];delete r.parts[0].json;return r;`, nil, false, `kind`},
		{"text_nonstring_payload", `const r=await tools.call("PictureA",{});r.parts=[r.parts[0]];r.parts[0].text={note:"text object"};return r;`, nil, false, "text object"},
		{"text_handle_field", `const r=await tools.call("PictureA",{});r.parts=[r.parts[0]];r.parts[0].handle="business handle";return r;`, nil, false, "business handle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			var bitmap bytes.Buffer
			if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			picture := func(name string) tool.Tool {
				return tool.NamedTool{Def: tool.Definition{Name: "Picture" + name, InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					return tool.Result{Content: []model.Part{model.NewTextPart("caption " + name), model.NewJSONPart([]byte(`{"value":42}`)), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(bitmap.Bytes())}, "image/png", name)}}, nil
				}}
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "return", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{picture("A"), picture("B"), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			var got []string
			failed := false
			directText, directJSON := false, false
			for _, e := range events {
				if e.Tool == nil || e.Tool.Name != codemode.ToolName || e.Message == nil {
					continue
				}
				for _, mp := range e.Message.Parts {
					if mp.ToolResult == nil {
						continue
					}
					failed = failed || mp.ToolResult.IsError
					for _, p := range mp.ToolResult.Content {
						if p.Media != nil {
							got = append(got, p.Media.Name)
						}
						if p.Text != nil && p.Text.Text == "caption A" {
							directText = true
						}
						if p.JSON != nil && string(p.JSON.Value) == `{"value":42}` {
							directJSON = true
						}
					}
				}
			}
			var modelMedia []string
			for _, req := range driver.requests {
				for _, message := range req.Messages {
					for _, part := range message.Parts {
						if part.ToolResult != nil {
							for _, content := range part.ToolResult.Content {
								if content.Media != nil {
									modelMedia = append(modelMedia, content.Media.Name)
								}
							}
						}
					}
				}
			}
			if tc.wantJSON != "" {
				if len(driver.requests) != 2 {
					t.Fatalf("next model request missing: %d", len(driver.requests))
				}
				raw, _ := json.Marshal(driver.requests[1].Messages)
				if !bytes.Contains(raw, []byte(tc.wantJSON)) {
					t.Fatalf("business data lost in next request: %s", raw)
				}
			}
			if strings.Join(modelMedia, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("unselected/historical media reached model: %v, want %v", modelMedia, tc.want)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || failed != tc.failed {
				t.Fatalf("returned media=%v failed=%v", got, failed)
			}
			if tc.name == "direct" && (!directText || !directJSON) {
				t.Fatal("direct text/JSON not delivered as model parts")
			}
		})
	}
}

func TestJSExecFixedFileBytesAndFreshVM(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte("actual input"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "script.js")
	if err = os.WriteFile(path, []byte(`const s=await tools.search("Read");if(s.count!==1||s.tools[0].name!=="Read"||!s.tools[0].input_schema)throw Error("native exact search");return await tools.call("Read",{path:"input.txt"});`), 0600); err != nil {
		t.Fatal(err)
	}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "first", Name: codemode.ToolName, Args: `{"file":"script.js"}`}}
		case 1:
			if err = os.WriteFile(path, []byte(`return {changed:true,process:typeof process};`), 0600); err != nil {
				t.Fatal(err)
			}
			return []model.ToolCall{{ID: "second", Name: codemode.ToolName, Args: `{"file":"script.js"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
	var hashes []string
	for _, e := range events {
		if e.Journal != nil && e.Journal.ToolExecution != nil && e.Journal.ToolExecution.ToolName == codemode.ToolName && e.Journal.ToolExecution.Status == "succeeded" {
			var result tool.Result
			if json.Unmarshal(e.Journal.ToolExecution.Result, &result) != nil {
				t.Fatal("invalid original result")
			}
			m, _ := result.Metadata["code_mode_source"].(map[string]any)
			if h, _ := m["sha256"].(string); h != "" {
				hashes = append(hashes, h)
			}
		}
	}
	if len(hashes) != 2 || hashes[0] == hashes[1] {
		t.Fatalf("source bytes/hash not reread: %v", hashes)
	}
}

func TestReviewSimpleReturnJSONDoesNotBecomeMedia(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"plain_kind", `return {kind:"media",label:"ordinary record"};`},
		{"plain_parts", `return {parts:["one","two"]};`},
		{"extracted_payload", `const r=await tools.call("Data",{});return r.parts[0].json;`},
		{"wrapped_payload_control", `return await tools.call("Data",{});`},
		{"search_schema_data", `return await tools.search("Data");`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			data := tool.NamedTool{Def: tool.Definition{Name: "Data", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"options": map[string]any{"type": "object", "default": map[string]any{"kind": "media", "handle": "data"}}}}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"kind":"media","handle":"ordinary-source-data"}`))}}, nil
			}}
			m := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "json-return", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: m, Tools: []tool.Tool{data, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					t.Fatalf("ordinary JSON rejected as control/media: %+v", event.Tool.Output)
				}
			}
		})
	}
}
