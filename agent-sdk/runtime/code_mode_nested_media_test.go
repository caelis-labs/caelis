package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// The provider is a local HTTP fixture; filesystem tools, worker, Runtime and
// next provider request encoding are real. No credential or live model is used.
func TestJSExecInjectedMediaInJSONReachesNextHTTPRequest(t *testing.T) {
	for _, tc := range []struct {
		name, selection, returned string
		want                      int
	}{
		{"wrapped", `image.parts.find(p=>p.kind==="media")`, `return {selected:json.parts[0]};`, 1},
		{"payload", `image.parts.find(p=>p.kind==="media")`, `return json.parts[0].json;`, 1},
		{"reply", `image.parts.find(p=>p.kind==="media")`, `return json;`, 1},
		{"copied", `({...image.parts.find(p=>p.kind==="media")})`, `return json;`, 0},
		{"ordinary", `{kind:"media",handle:"ordinary-business-data"}`, `return {selected:json.parts[0]};`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			view, err := filesystem.NewViewImage(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			bitmap := image.NewRGBA(image.Rect(0, 0, 96, 64))
			for y := 0; y < 64; y++ {
				for x := 0; x < 96; x++ {
					bitmap.SetRGBA(x, y, color.RGBA{R: 225, G: 245, B: 245, A: 255})
					if x > 20 && x < 76 && y > 12 && y < 52 {
						bitmap.SetRGBA(x, y, color.RGBA{G: 170, B: 170, A: 255})
					}
				}
			}
			var pngBytes bytes.Buffer
			if err = png.Encode(&pngBytes, bitmap); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "fixture.png"), pngBytes.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte(`{"case":"native-read"}`), 0600); err != nil {
				t.Fatal(err)
			}
			code := `const image=await tools.call("ViewImage",{path:"fixture.png"});const json=await tools.call("Read",{path:"input.txt"});json.parts[0].json.selected=` + tc.selection + `;` + tc.returned
			args, _ := json.Marshal(map[string]any{"code": code})
			var mu sync.Mutex
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Error(readErr)
					http.Error(w, "read", 500)
					return
				}
				mu.Lock()
				requests = append(requests, raw)
				n := len(requests)
				mu.Unlock()
				output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
				if n == 1 {
					output = []any{map[string]any{"type": "function_call", "name": "js_exec", "call_id": "injected-media", "arguments": string(args)}}
				}
				if n > 2 {
					t.Error("unexpected extra request")
					http.Error(w, "extra", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture-response", "object": "response", "model": "fixture-model", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
			}))
			defer server.Close()
			factory := providers.NewFactory()
			if err = factory.Register(providers.Config{Alias: "fixture/picture", Provider: "fixture", API: providers.APIOpenAIResponses, Model: "fixture-model", BaseURL: server.URL, HTTPClient: server.Client(), Auth: providers.AuthConfig{Type: providers.AuthNone}, ImageInput: true, ContextWindowTokens: 200000}); err != nil {
				t.Fatal(err)
			}
			llm, err := factory.NewByAlias("fixture/picture")
			if err != nil {
				t.Fatal(err)
			}
			runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{read, view, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}}, nil, nil)
			mu.Lock()
			wire := append([][]byte(nil), requests...)
			mu.Unlock()
			if len(wire) != 2 {
				t.Fatalf("HTTP requests=%d", len(wire))
			}
			if dir := os.Getenv("CAELIS_CODE_MODE_P2_ARTIFACTS"); dir != "" {
				dir = filepath.Join(dir, tc.name)
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for i, raw := range wire {
					if err = os.WriteFile(filepath.Join(dir, []string{"first.request.json", "next.request.json"}[i]), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err = os.WriteFile(filepath.Join(dir, "source.png"), pngBytes.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var next any
			if err = json.Unmarshal(wire[1], &next); err != nil {
				t.Fatal(err)
			}
			count := 0
			var walk func(any)
			walk = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					for _, item := range x {
						walk(item)
					}
				case []any:
					for _, item := range x {
						walk(item)
					}
				case string:
					const prefix = "data:image/png;base64,"
					if len(x) >= len(prefix) && x[:len(prefix)] == prefix {
						count++
						actual, e := base64.StdEncoding.DecodeString(x[len(prefix):])
						if e != nil || !bytes.Equal(actual, pngBytes.Bytes()) {
							t.Fatal("actual next HTTP image bytes differ from native ViewImage PNG")
						}
					}
				}
			}
			walk(next)
			if count != tc.want {
				t.Fatalf("next provider HTTP has %d images; want %d (JSON handle is not media delivery)", count, tc.want)
			}
			t.Logf("next physical HTTP: images=%d original PNG bytes=%d; exact byte match", count, pngBytes.Len())
		})
	}
}
