package gatewayapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// Functional acceptance only: two fresh Sessions, no cost pairs, no retries or
// synthetic provider answers. Network traces retain each original stream/receipt.
func TestCodeModeSimpleLive(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_SIMPLE_LIVE")
	if root == "" {
		t.Skip("explicit task-owned functional acceptance directory required")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute artifact directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	started, err := os.OpenFile(filepath.Join(root, "RUN-STARTED"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("run already started; inspect original results instead of replaying")
	}
	started.Close()
	ctx := t.Context()
	cfg, cleanup, err := selectedCodeModeModel(ctx, os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if (cfg.Timeout > 0 && cfg.Timeout < 600*time.Second) || (cfg.StreamFirstEventTimeout > 0 && cfg.StreamFirstEventTimeout < 600*time.Second) {
		t.Fatal("configured shorter model timeout; coordinate before running")
	}
	writeLiveJSON(t, filepath.Join(root, "model.json"), fairModelParameters(cfg))
	var headerless = http.DefaultTransport.(*http.Transport).Clone()
	headerless.ResponseHeaderTimeout = 0
	workspace := filepath.Join(root, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	spec, err := fairContext7Spec(os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CONTEXT7_ENTRY"), workspace)
	if err != nil {
		t.Fatal(err)
	}
	dtwRoot := os.Getenv("CAELIS_CODE_MODE_LIVE_DTW_ROOT")
	if !filepath.IsAbs(dtwRoot) {
		t.Fatal("explicit task-owned existing DTW package root required")
	}
	specs := []mcp.ServerSpec{spec, {PluginID: "desktop-world", Name: "desktop-world", Transport: mcp.TransportStdio, Command: filepath.Join(dtwRoot, "bin", "dtw"), Args: []string{"mcp"}, WorkDir: dtwRoot, CleanEnvironment: true}}
	manager, err := mcp.NewManager(ctx, specs, func(f mcp.ServerFailure) { t.Logf("MCP failure source=%s code=%s", f.Name, errorcode.CodeOf(f.Err)) })
	if err != nil {
		t.Fatal("MCP manager initialization failed")
	}
	defer func() {
		before, _ := exec.Command("/bin/ps", "-axo", "pid=,ppid=,comm=").Output()
		_ = os.WriteFile(filepath.Join(root, "processes-before-close.txt"), simpleOwnedProcesses(before, os.Getpid(), root), 0600)
		closeErr := manager.Close()
		writeLiveJSON(t, filepath.Join(root, "manager-close.json"), map[string]any{"error_code": errorcode.CodeOf(closeErr)})
		p, _ := exec.Command("/bin/ps", "-axo", "pid=,ppid=,comm=").Output()
		_ = os.WriteFile(filepath.Join(root, "processes-after-close.txt"), simpleOwnedProcesses(p, os.Getpid(), root), 0600)
	}()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	p, _ := exec.Command("/bin/ps", "-axo", "pid=,ppid=,comm=").Output()
	_ = os.WriteFile(filepath.Join(root, "processes-initialized.txt"), simpleOwnedProcesses(p, os.Getpid(), root), 0600)
	var defs []tool.Definition
	for _, one := range manager.SearchTools() {
		defs = append(defs, one.Definition())
	}
	writeLiveJSON(t, filepath.Join(root, "directory.json"), defs)
	allowed := func(def tool.Definition, raw json.RawMessage) bool {
		if def.ExecutionRequirements != nil && def.ExecutionRequirements.Sandbox.CommandExec {
			return false
		}
		if !tool.IsMCPDefinition(def) {
			return true
		}
		remote, _ := def.Metadata[tool.MetadataMCPTool].(string)
		switch remote {
		case "resolve-library-id", "query-docs", "get-library-docs", "apps":
			return true
		case "exec":
			var v struct {
				App    string `json:"app"`
				Source string `json:"source"`
			}
			return json.Unmarshal(raw, &v) == nil && (v.App == "dev.caelis.codemode.fixture" || v.App == "CodeModeFixture") && simpleReadonlyObservation(v.Source)
		}
		return false
	}
	policies, err := policy.NewMemory(policy.NamedMode{ID: "simple-readonly", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		if !allowed(in.Tool, in.Call.Input) {
			return policy.Decision{Action: policy.ActionDeny, Reason: "outside task-owned read-only acceptance scope"}, nil
		}
		var args map[string]any
		_ = json.Unmarshal(in.Call.Input, &args)
		for _, key := range []string{"path", "file"} {
			if path, ok := args[key].(string); ok {
				if !filepath.IsAbs(path) {
					path = filepath.Join(workspace, path)
				}
				rel, e := filepath.Rel(workspace, path)
				if e != nil {
					return policy.Decision{}, e
				}
				if rel == ".." || strings.HasPrefix(rel, "../") {
					return policy.Decision{Action: policy.ActionDeny, Reason: "outside task workspace"}, nil
				}
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	requester := liveApprovalFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if allowed(r.Tool, r.Call.Input) {
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
	})
	prompts := []struct{ ID, Text string }{
		{"context7", `Use js_exec for an actual Context7 documentation call. Find React useEffect cleanup documentation: when is cleanup run? Return the tool result, then give a brief supported answer and its actual source, or explicitly report unavailable. Use read-only Context7 only; no shell or file writes.`},
		{"image", `Use js_exec and Desktop World only to observe and capture the already running task-owned CodeModeFixture (bundle dev.caelis.codemode.fixture). Only this read-only domain source is authorized: await app.look(); await app.capture(); . Do not operate any other application or send input. Return the actual image result to yourself and describe the colored geometric marker and the large code visibly drawn in the fixture. Do not infer the picture from tool names, window title or this request. No shell or file writes.`},
	}
	if selected := os.Getenv("CAELIS_CODE_MODE_SIMPLE_ONLY"); selected != "" {
		if selected != "image" {
			t.Fatal("unsupported acceptance selection")
		}
		prompts = prompts[1:]
	}
	writeLiveJSON(t, filepath.Join(root, "prompts.json"), prompts)
	for _, task := range prompts {
		dir := filepath.Join(root, task.ID)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		localCfg := cfg
		localCfg.HTTPClient = &http.Client{Transport: &fairTransport{base: headerless, root: dir}}
		built, e := modelconfig.BuildModel(localCfg, 0, 0)
		if e != nil {
			t.Fatal("selected model construction failed")
		}
		trace := &fairTrace{LLM: built.Model, root: dir, turnCalls: map[int]int{}}
		sandbox, e := host.New(host.Config{CWD: workspace})
		if e != nil {
			t.Fatal(e)
		}
		tools, e := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandbox})
		if e != nil {
			t.Fatal(e)
		}
		for _, one := range tools {
			if js, ok := one.(*codemode.Tool); ok {
				js.Executor = &gojaexec.Executor{Observe: func(m gojaexec.Metrics) {
					f, e := os.OpenFile(filepath.Join(dir, "workers.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if e == nil {
						_, _ = f.Write(append(fairJSON(m), '\n'))
						_ = f.Close()
					}
				}}
			}
		}
		tools = append(tools, toolsearch.NewSource(manager))
		store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(dir, "sessions")})
		active, e := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "code-mode-simple", Workspace: session.WorkspaceRef{Key: dir, CWD: workspace}})
		if e != nil {
			t.Fatal(e)
		}
		writeLiveJSON(t, filepath.Join(dir, "session-ref.json"), active.SessionRef)
		grants, e := runtime.NewMCPGrantStore(filepath.Join(dir, "grants.json"))
		if e != nil {
			t.Fatal(e)
		}
		rt, e := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{SystemPrompt: fairSystem}, PolicyRegistry: policies, DefaultPolicyMode: "simple-readonly", MCPGrants: grants})
		if e != nil {
			t.Fatal(e)
		}
		taskCtx, cancelTask := context.WithCancel(ctx)
		defer cancelTask()
		observer := agent.SourceEventObserverFunc(func(_ context.Context, e agent.SourceEvent) error {
			if e.Canonical == nil {
				return nil
			}
			if e.Canonical.Journal != nil && e.Canonical.Journal.ToolExecution != nil && e.Canonical.Journal.ToolExecution.Status == session.ToolExecutionUnknownOutcome {
				defer cancelTask()
			}
			f, e1 := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if e1 != nil {
				return e1
			}
			_, e1 = f.Write(append(fairJSON(e.Canonical), '\n'))
			return errors.Join(e1, f.Close())
		})
		stream := true
		result, runErr := rt.Run(taskCtx, agent.RunRequest{SessionRef: active.SessionRef, Input: task.Text, Request: agent.ModelRequestOptions{Stream: &stream}, ApprovalRequester: requester, SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: trace, Metadata: map[string]any{"reasoning_effort": built.ReasoningEffort, codemode.MetadataSelectedTools: gatewayapp.CodeModeFixtureSelectedTools(manager.Tools())}, Tools: tools, DeferredTools: manager}})
		if runErr == nil {
			runErr = result.Handle.WaitCompletion(ctx)
		}
		history, loadErr := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if loadErr == nil {
			writeLiveJSON(t, filepath.Join(dir, "history.json"), history)
		}
		_ = sandbox.Close()
		writeLiveJSON(t, filepath.Join(dir, "terminal.json"), map[string]any{"run_error_code": errorcode.CodeOf(runErr), "run_error_type": fmt.Sprintf("%T", runErr), "load_error_code": errorcode.CodeOf(loadErr), "model_generations": trace.calls})
		if runErr != nil || loadErr != nil {
			t.Fatal("functional run failed; original receipts retained, no retry: " + string(errorcode.CodeOf(runErr)))
		}
		checks := simpleFunctionalChecks(t, dir, task.ID)
		writeLiveJSON(t, filepath.Join(dir, "functional-assertions.json"), checks)
		for name, ok := range checks {
			if !ok {
				t.Fatalf("functional assertion failed: %s; original sample retained", name)
			}
		}
	}
}

// Retain task-owned subprocess identities and package paths, never argv/environment.
func simpleOwnedProcesses(raw []byte, parent int, root string) []byte {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		var pid, ppid int
		var name string
		if _, err := fmt.Sscanf(line, "%d %d %s", &pid, &ppid, &name); err == nil && (ppid == parent || strings.HasPrefix(name, root+string(filepath.Separator))) && filepath.Base(name) != "ps" {
			out = append(out, fmt.Sprintf("%d %d %s", pid, ppid, name))
		}
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// Outcome assertions are independent of a model's successful Run termination.
func simpleFunctionalChecks(t *testing.T, dir, id string) map[string]bool {
	t.Helper()
	requests, err := filepath.Glob(filepath.Join(dir, "generation-*.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	imageReceived := false
	for _, path := range requests {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var req model.Request
		if json.Unmarshal(raw, &req) != nil {
			t.Fatal("invalid retained request")
		}
		for _, message := range req.Messages {
			for _, part := range message.Parts {
				if part.ToolResult != nil {
					for _, content := range part.ToolResult.Content {
						if content.Media != nil && content.Media.Modality == model.MediaModalityImage {
							imageReceived = true
						}
					}
				}
			}
		}
	}
	responses, err := filepath.Glob(filepath.Join(dir, "generation-*.response.json"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(responses)
	final := ""
	for _, path := range responses {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var response model.Response
		if json.Unmarshal(raw, &response) != nil {
			t.Fatal("invalid retained Response")
		}
		if response.FinishReason == model.FinishReasonStop {
			for _, part := range response.Message.Parts {
				if part.Text != nil {
					final = part.Text.Text
				}
			}
		}
	}
	lower := strings.ToLower(final)
	if id == "image" {
		return map[string]bool{"actual_image_in_following_model_request": imageReceived, "visible_code": strings.Contains(final, "ORBIT") && strings.Contains(final, "582"), "shape": strings.Contains(lower, "triangle") || strings.Contains(final, "三角"), "color": strings.Contains(lower, "teal") || strings.Contains(lower, "cyan") || strings.Contains(lower, "turquoise") || strings.Contains(final, "青") || strings.Contains(final, "蓝绿")}
	}
	return map[string]bool{"sourced_api_answer": strings.Contains(lower, "cleanup") && strings.Contains(lower, "react") && strings.Contains(final, "https://")}
}

// This acceptance fixture permits only two read-only calls and returning their
// bound results. It is not a validator for arbitrary Desktop World programs.
var simpleReadonlyExact = regexp.MustCompile(`^\s*await\s+app\.look\(\)\s*;\s*await\s+app\.capture\(\)\s*;?\s*$`)
var simpleReadonlyBound = regexp.MustCompile(`^\s*const\s+([A-Za-z_$][A-Za-z0-9_$]{0,63})\s*=\s*await\s+app\.look\(\)\s*;\s*const\s+([A-Za-z_$][A-Za-z0-9_$]{0,63})\s*=\s*await\s+app\.capture\(\)\s*;\s*return\s+(?:\{\s*([A-Za-z_$][A-Za-z0-9_$]{0,63})\s*,\s*([A-Za-z_$][A-Za-z0-9_$]{0,63})\s*\}|([A-Za-z_$][A-Za-z0-9_$]{0,63}))\s*;?\s*$`)

func simpleReadonlyObservation(source string) bool {
	if simpleReadonlyExact.MatchString(source) {
		return true
	}
	m := simpleReadonlyBound.FindStringSubmatch(source)
	return m != nil && m[1] != m[2] && ((m[3] == m[1] && m[4] == m[2]) || m[5] == m[2])
}
func TestCodeModeSimpleReadOnlyBodies(t *testing.T) {
	for _, source := range []string{`await app.look(); await app.capture();`, `const look = await app.look(); const capture = await app.capture(); return {look, capture};`, `const looked = await app.look(); const image = await app.capture(); return {looked, image};`, `const a=await app.look();const b=await app.capture();return b;`} {
		if !simpleReadonlyObservation(source) {
			t.Fatalf("authorized body rejected: %s", source)
		}
	}
	for _, source := range []string{`await app.look();await app.capture();app.click(1,2);`, `const a=await app.look();const b=await app.capture();return {a,other};`, `const a=await app.look();const a=await app.capture();return {a,a};`, `const a=await app.look();const b=await app.capture();return eval(b);`, `await app.quit();`, `const a=await app.look();const b=await app.capture();return {a,b}; process.exit();`} {
		if simpleReadonlyObservation(source) {
			t.Fatalf("out-of-scope body admitted: %s", source)
		}
	}
}
