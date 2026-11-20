package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeNativeBusinessResult(t *testing.T) {
	for _, tc := range []struct {
		name, code, wantContent string
		wantErrorCode           string
	}{
		{"read", `
const {data: file} = await Read({path: "input.txt"});
return {content: file.content, done: !file.has_more};`, "1: business-value", ""},
		{"caught_failure", `
let failure;
try {
  await Read({path: "missing.txt"});
} catch (e) {
  failure = {code: e.code, message: e.message};
}
const {data: file} = await Read({path: "input.txt"});
return {content: file.content, done: !file.has_more, failure};`, "1: business-value", "not_found"},
		{"grep_then_read", `
const {data: matches} = await Grep({pattern: "follow-up-value", include: "*.txt"});
if (matches.hits.length !== 1) throw Error("expected one matching line");
const hit = matches.hits[0];
const {data: file} = await Read({path: hit.path, offset: hit.line - 1, limit: 1});
return {content: file.content, done: !file.has_more};`, "2: follow-up-value", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), backend)
			if err != nil {
				t.Fatal(err)
			}
			grep, err := filesystem.NewSearch(backend)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte("business-value"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "other.txt"), []byte("header\nfollow-up-value"), 0600); err != nil {
				t.Fatal(err)
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": tc.code})
					return []model.ToolCall{{ID: "business", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{read, grep, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("requests=%d", len(driver.requests))
			}
			visible := errorDeliveryModelResult(t, driver.requests[1], "business")
			raw := errorDeliveryJSON(t, visible.Content)
			if visible.IsError != (tc.wantErrorCode != "") || !bytes.Contains(raw, []byte(`"content":"`+tc.wantContent+`"`)) || !bytes.Contains(raw, []byte(`"done":true`)) {
				t.Fatalf("business result not available: %s", raw)
			}
			if tc.wantErrorCode != "" && !bytes.Contains(raw, []byte(`"code":"`+tc.wantErrorCode+`"`)) {
				t.Fatalf("caught error code not available: %s", raw)
			}
		})
	}
}
