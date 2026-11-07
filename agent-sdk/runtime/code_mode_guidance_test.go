package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/dop251/goja"
)

// These checks evaluate prose examples with inert JSON samples, never a model,
// Runtime Tool, Host, worker, MCP connection or native filesystem Tool call.
func TestCodeModeGuidanceExamplesOffline(t *testing.T) {
	store, active, _ := codeModeStore(t)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := rt.codeModeCatalog(t.Context(), active.SessionRef, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Native fs functions already in tools: call them directly", "No shell/process in JS", "Only unknown extension names need search", "tools.call(name,args)", "parts[kind=json].json", "compute/filter/group/count in VM", "required exact strings unchanged", "reuse schemas across Turns"} {
		if !strings.Contains(text, required) {
			t.Errorf("missing guidance: %q", required)
		}
	}
	if strings.Contains(text, "output.text(await tools.call") {
		t.Fatal("default call example forwards the entire reply")
	}
	lines := strings.Split(text, "\n")
	var example string
	for i, line := range lines {
		if strings.HasPrefix(line, "const r=await tools.call(name,args)") && i+1 < len(lines) {
			example = line + "\n" + lines[i+1]
		}
	}
	if example == "" {
		t.Fatal("missing executable projection example")
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "code-mode.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(doc), "```js\n")
	if len(blocks) != 4 {
		t.Fatal("expected projection, optional fs calculation and image examples")
	}
	docProjection := strings.SplitN(blocks[1], "```", 2)[0]
	docCalculation := strings.SplitN(blocks[2], "```", 2)[0]
	type sample struct {
		Reply    json.RawMessage `json:"reply"`
		Expected json.RawMessage `json:"expected"`
	}
	samples := []sample{{json.RawMessage(`{"parts":[{"kind":"text","text":"not the JSON payload"},{"kind":"json","json":{"batch_id":"sample","records":[{},{}]}}]}`), json.RawMessage(`{"count":2,"source":"sample"}`)}}
	if path := os.Getenv("CAELIS_CODE_MODE_GUIDANCE_SAMPLES"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var captured []sample
		if err := json.Unmarshal(raw, &captured); err != nil || len(captured) == 0 {
			t.Fatalf("invalid captured reply samples: %v", err)
		}
		samples = append(samples, captured...)
	}
	for _, s := range samples {
		prelude := `const name="sample__read",args={query:"Keep: A / B?"},selected=[],calls=[];
const reply=` + string(s.Reply) + `;
const tools={call:async(n,a)=>{calls.push({name:n,args:a});return reply;}};
const output={text:v=>selected.push(v)};`
		want := `{"selected":[` + string(s.Expected) + `],"calls":[{"name":"sample__read","args":{"query":"Keep: A / B?"}}]}`
		for _, code := range []string{example, docProjection} {
			guidanceExampleJSON(t, prelude, code, want)
		}
	}
	guidanceExampleJSON(t, `const selected=[],calls=[];
const tools={call:async(n,a)=>{calls.push({name:n,args:a});return {parts:[{kind:"text",text:"not JSON"},{kind:"json",json:{content:a.path==="a.txt"?"3":"5"}}]};}};
const output={text:v=>selected.push(v)};`, docCalculation,
		`{"selected":[{"total":8,"sources":["a.txt","b.txt"]}],"calls":[{"name":"Read","args":{"path":"a.txt"}},{"name":"Read","args":{"path":"b.txt"}}]}`)
	t.Logf("pure JSON sample projections checked: %d; no Tool/model dispatch", len(samples))
}

func guidanceExampleJSON(t *testing.T, prelude, code, want string) {
	t.Helper()
	vm := goja.New()
	value, err := vm.RunString(prelude + "\n(async()=>{\n" + code + "\n})()")
	if err != nil {
		t.Fatal(err)
	}
	promise, ok := value.Export().(*goja.Promise)
	if !ok || promise.State() != goja.PromiseStateFulfilled {
		t.Fatalf("example did not finish: %v", value)
	}
	actual, err := vm.RunString(`JSON.stringify({selected,calls})`)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if err := json.Unmarshal([]byte(actual.String()), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(expected)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("projection mismatch: got %s want %s", gotJSON, wantJSON)
	}
}
