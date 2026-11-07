package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

const authorizedModelID = "magpie@local/magpie/codex/gpt-6-luna"
const flashModelID = "magpie@local/magpie/deepseek/deepseek-flash"

func authorizedModelName(id string) string {
	switch id {
	case authorizedModelID:
		return "codex/gpt-6-luna"
	case flashModelID:
		return "deepseek/deepseek-flash"
	}
	return ""
}

// Opt-in real model + actual local Runtime/MCP acceptance. All model-generated
// source and original receipts are retained. Production configuration is read
// only for the named model/endpoint/credential and Context7 entry; never exported.
func TestCodeModeLive(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_LIVE_ARTIFACTS")
	if root == "" {
		t.Skip("requires authorized live acceptance artifacts directory")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute task-owned artifacts directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	selected := os.Getenv("CAELIS_CODE_MODE_LIVE_MODEL")
	if selected == "" {
		selected = authorizedModelID
	}
	mode := os.Getenv("CAELIS_CODE_MODE_LIVE_MODE")
	if mode != "baseline" && mode != "js_exec" {
		t.Fatal("select baseline or js_exec mode")
	}
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), selected)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// No Client.Timeout or shorter outer/header context. Each Generate below has
	// its own 600-second budget, including selector generations; no client retry.
	cfg.HTTPClient = &http.Client{Transport: &codeLiveTransport{base: http.DefaultTransport, root: root}}
	resolved, err := modelconfig.BuildModel(cfg, 0, 0)
	if err != nil {
		t.Fatal("model construction: " + string(errorcode.CodeOf(err)))
	}
	trace := &codeLiveTrace{LLM: resolved.Model, root: root}
	writeLiveJSON(t, filepath.Join(root, "model.json"), map[string]any{"id": cfg.ID, "model": cfg.Model, "provider": cfg.Provider, "api": cfg.API, "endpoint_id": cfg.ProviderEndpointID, "reasoning_effort": cfg.ReasoningEffort, "reasoning_mode": cfg.ReasoningMode, "stream": true, "per_generation_budget_seconds": 600, "mode": mode})
	stateRoot := os.Getenv("CAELIS_CODE_MODE_LIVE_STATE_ROOT")
	if stateRoot == "" {
		stateRoot = root
	}
	if !filepath.IsAbs(stateRoot) {
		t.Fatal("absolute task-owned state root required")
	}
	resume := os.Getenv("CAELIS_CODE_MODE_LIVE_RESUME") == "1"
	workspace := filepath.Join(stateRoot, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if !resume {
		_ = os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("17\n"), 0600)
		_ = os.WriteFile(filepath.Join(workspace, "b.txt"), []byte("25\n"), 0600)
	}
	var doc struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcp_servers"`
	}
	raw, err := os.ReadFile(os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"))
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatal("Context7 configuration unavailable")
	}
	server, ok := doc.Servers["context7"]
	if !ok {
		t.Fatal("exact Context7 entry missing")
	}
	cached := os.Getenv("CAELIS_CODE_MODE_LIVE_CONTEXT7_ENTRY")
	if !filepath.IsAbs(cached) {
		t.Fatal("explicit existing Context7 module entry required; no installer")
	}
	args := []string{cached}
	skipPackage := false
	for _, arg := range server.Args {
		if arg == "-y" || arg == "--yes" {
			continue
		}
		if strings.HasPrefix(arg, "@upstash/context7-mcp") {
			skipPackage = true
			continue
		}
		args = append(args, arg)
	}
	if !skipPackage {
		t.Fatal("unexpected Context7 launch configuration")
	}
	noGUI := os.Getenv("CAELIS_CODE_MODE_LIVE_ENABLE_GUI") != "1" || os.Getenv("CAELIS_CODE_MODE_LIVE_NO_GUI") == "1"
	specs := []mcp.ServerSpec{{PluginID: "context7", Name: "context7", Transport: mcp.TransportStdio, Command: "node", Args: args, Env: server.Env, WorkDir: workspace, CleanEnvironment: true}}
	if !noGUI {
		dtwRoot := os.Getenv("CAELIS_CODE_MODE_LIVE_DTW_ROOT")
		if !filepath.IsAbs(dtwRoot) {
			t.Fatal("existing managed DTW root required")
		}
		specs = append(specs, mcp.ServerSpec{PluginID: "desktop-world", Name: "desktop-world", Transport: mcp.TransportStdio, Command: filepath.Join(dtwRoot, "bin", "dtw"), Args: []string{"mcp"}, WorkDir: dtwRoot, CleanEnvironment: true})
	}
	manager, err := mcp.NewManager(t.Context(), specs, func(f mcp.ServerFailure) { t.Logf("MCP failure source=%s code=%s", f.Name, errorcode.CodeOf(f.Err)) })
	if err != nil {
		t.Fatal("MCP manager failed")
	}
	defer manager.Close()
	select {
	case <-manager.Initialized():
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	var defs []tool.Definition
	for _, one := range manager.SearchTools() {
		defs = append(defs, one.Definition())
	}
	writeLiveJSON(t, filepath.Join(root, "directory.json"), defs)
	if len(defs) < 2 {
		t.Fatal("Context7/DTW directory incomplete")
	}
	sandbox, err := host.New(host.Config{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandbox})
	if err != nil {
		t.Fatal(err)
	}
	for _, one := range tools {
		if script, ok := one.(*codemode.Tool); ok {
			script.Executor = &gojaexec.Executor{Observe: func(metrics gojaexec.Metrics) {
				raw, _ := json.Marshal(metrics)
				f, e := os.OpenFile(filepath.Join(root, "worker-metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if e == nil {
					_, _ = f.Write(append(raw, '\n'))
					_ = f.Close()
				}
			}}
		}
	}
	if mode == "baseline" {
		kept := tools[:0]
		for _, one := range tools {
			if one.Definition().Name != codemode.ToolName {
				kept = append(kept, one)
			}
		}
		tools = kept
	}
	tools = append(tools, toolsearch.NewSource(manager))
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(stateRoot, "sessions")})
	var active session.Session
	if resume {
		raw, err := os.ReadFile(filepath.Join(stateRoot, "active-session.json"))
		if err != nil {
			t.Fatal("original task-owned Session identity missing")
		}
		var ref session.SessionRef
		if json.Unmarshal(raw, &ref) != nil {
			t.Fatal("invalid original Session identity")
		}
		loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
		if err != nil {
			t.Fatal(err)
		}
		active = loaded.Session
	} else {
		active, err = store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "code-mode-live", Workspace: session.WorkspaceRef{Key: stateRoot, CWD: workspace}})
		if err != nil {
			t.Fatal(err)
		}
		writeLiveJSON(t, filepath.Join(stateRoot, "active-session.json"), active.SessionRef)
	}
	grants, err := runtime.NewMCPGrantStore(filepath.Join(stateRoot, "mcp-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	readOnlySource := regexp.MustCompile(`^\s*await\s+app\.look\(\)\s*;\s*await\s+app\.capture\(\)\s*;?\s*$`)
	registry, err := policy.NewMemory(policy.NamedMode{ID: "code-mode-local", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		if in.Tool.Name == "RunCommand" {
			return policy.Decision{Action: policy.ActionDeny, Reason: "acceptance scope excludes shell"}, nil
		}
		if tool.IsMCPDefinition(in.Tool) {
			remote, _ := in.Tool.Metadata[tool.MetadataMCPTool].(string)
			allowed := remote == "resolve-library-id" || remote == "query-docs" || remote == "get-library-docs" || remote == "apps"
			if remote == "exec" {
				var v struct {
					App    string `json:"app"`
					Source string `json:"source"`
				}
				allowed = json.Unmarshal(in.Call.Input, &v) == nil && (v.App == "dev.caelis.codemode.fixture" || v.App == "CodeModeFixture") && readOnlySource.MatchString(v.Source)
			}
			if !allowed {
				return policy.Decision{Action: policy.ActionDeny, Reason: "outside fixture read-only scope"}, nil
			}
		}
		var paths map[string]any
		_ = json.Unmarshal(in.Call.Input, &paths)
		for _, key := range []string{"path", "file"} {
			if v, ok := paths[key].(string); ok && v != "" {
				target := v
				if !filepath.IsAbs(target) {
					target = filepath.Join(workspace, target)
				}
				relative, e := filepath.Rel(workspace, target)
				if e != nil {
					return policy.Decision{}, e
				}
				if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return policy.Decision{Action: policy.ActionDeny, Reason: "outside task workspace"}, nil
				}
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{SystemPrompt: "Complete the user's task with actual tools. Do not invent tool output. Use only the task-owned workspace and the explicitly named fixture application. Do not use shell or mutate external services. No automatic replay after an unknown effect."}, PolicyRegistry: registry, DefaultPolicyMode: "code-mode-local", MCPGrants: grants})
	if err != nil {
		t.Fatal(err)
	}

	requester := liveApprovalFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approved := false
		remote, _ := r.Tool.Metadata[tool.MetadataMCPTool].(string)
		switch remote {
		case "resolve-library-id", "query-docs", "get-library-docs", "apps":
			approved = true
		case "exec":
			var v struct {
				App    string `json:"app"`
				Source string `json:"source"`
			}
			if json.Unmarshal(r.Call.Input, &v) == nil {
				approved = (v.App == "dev.caelis.codemode.fixture" || v.App == "CodeModeFixture") && readOnlySource.MatchString(v.Source)
			}
		}
		if approved {
			option := "allow_once"
			for _, candidate := range r.Approval.Options {
				if candidate.ID == "allow_session" {
					option = candidate.ID
					break
				}
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: true}, nil
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
	})
	route := "Use native filesystem tools and the existing ToolSearch/mcp_call extension entries."
	if mode == "js_exec" {
		route = "Use js_exec with tools.describe/search/call for extensions and JS orchestration. Native filesystem calls may write/edit workflow.js; execute the actual saved file with js_exec file. Select only the aggregate/documentation and screenshot outputs. Shell must remain native and is forbidden for this task."
	}
	prompts := []string{
		route + " Read a.txt and b.txt using actual tools and aggregate their numbers. Query Context7 for Goja Promise/Runtime.Interrupt API documentation using actual resolve-library-id and query-docs; report its documentation source without inventing availability. Save an executable workflow.js (js_exec async body with tools.call) which reads both files, returns their total (42), and writes answer.json containing total and a brief sourced API summary. Execute it from file. In baseline mode, complete the same data/documentation task with native tools and also save answer.json; workflow.js execution is required only in js_exec mode.",
		route + " Continue in this same Session. For js_exec, edit the same workflow.js bytes so the returned total adds 1 to the two read values, and execute that same file again; answer.json must now contain total 43. Baseline: update answer.json to total 43 using the same read values plus 1. Then use Desktop World apps discovery for CodeModeFixture (bundle dev.caelis.codemode.fixture), and exec scoped exactly to that fixture. Only this exact read-only DTW domain body is authorized: await app.look(); await app.capture(); . Do not use other applications or input methods. Show the actual fixture screenshot to the model (js_exec: output.image with its returned opaque handle), and report the observed fixture title and synthetic values. No writes outside this workspace.",
	}
	writeLiveJSON(t, filepath.Join(root, "prompts.json"), prompts)
	observer := agent.SourceEventObserverFunc(func(_ context.Context, e agent.SourceEvent) error {
		if e.Canonical == nil {
			return nil
		}
		raw, _ := json.Marshal(e.Canonical)
		f, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(append(raw, '\n'))
			f.Close()
		}
		return err
	})
	stream := true
	for i, prompt := range prompts {
		if resume && i == 0 {
			continue
		}
		if noGUI && i == 1 {
			writeLiveJSON(t, filepath.Join(root, "desktop-pending.json"), map[string]any{"reason": "shared desktop paused by coordinator", "task": 2})
			break
		}
		result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: prompt, Request: agent.ModelRequestOptions{Stream: &stream}, ApprovalRequester: requester, SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: trace, Metadata: map[string]any{"reasoning_effort": resolved.ReasoningEffort}, Tools: tools, DeferredTools: manager}})
		if err == nil {
			err = result.Handle.WaitCompletion(t.Context())
		}
		if err != nil {
			message := err.Error()
			if cfg.Token != "" {
				message = strings.ReplaceAll(message, cfg.Token, "[redacted]")
			}
			writeLiveJSON(t, filepath.Join(root, "failure.json"), map[string]any{"task": i + 1, "error_code": errorcode.CodeOf(err), "error_type": fmt.Sprintf("%T", err), "message": message})
			t.Fatal("real model task failed: " + string(errorcode.CodeOf(err)))
		}
		answer, err := os.ReadFile(filepath.Join(workspace, "answer.json"))
		if err != nil {
			t.Fatal("model did not write answer.json")
		}
		var v struct {
			Total int `json:"total"`
		}
		if json.Unmarshal(answer, &v) != nil || v.Total != 42+i {
			t.Fatalf("wrong actual aggregate at task %d", i+1)
		}
		if mode == "js_exec" {
			scriptBytes, e := os.ReadFile(filepath.Join(workspace, "workflow.js"))
			if e != nil {
				t.Fatal("generated executable source missing")
			}
			sum := sha256.Sum256(scriptBytes)
			expected := hex.EncodeToString(sum[:])
			history, e := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if e != nil {
				t.Fatal(e)
			}
			fixedFile := false
			for _, event := range history.Events {
				if event.Journal == nil || event.Journal.ToolExecution == nil {
					continue
				}
				record := event.Journal.ToolExecution
				if record.ToolName != codemode.ToolName || record.Status != session.ToolExecutionSucceeded {
					continue
				}
				var input struct {
					File string `json:"file"`
				}
				_ = json.Unmarshal(record.Input, &input)
				if filepath.Base(input.File) != "workflow.js" {
					continue
				}
				var result tool.Result
				if json.Unmarshal(record.Result, &result) != nil {
					continue
				}
				source, _ := result.Metadata["code_mode_source"].(map[string]any)
				if source["sha256"] == expected {
					fixedFile = true
				}
			}
			if !fixedFile {
				t.Fatal("no original successful fixed-file execution receipt matches current workflow bytes")
			}
			if i == 1 {
				before, e := os.ReadFile(filepath.Join(stateRoot, "task-1-workflow.js"))
				if e != nil || string(before) == string(scriptBytes) {
					t.Fatal("second task did not edit original file bytes")
				}
			}
		}
		for _, name := range []string{"workflow.js", "answer.json"} {
			if b, err := os.ReadFile(filepath.Join(workspace, name)); err == nil {
				_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("task-%d-%s", i+1, name)), b, 0600)
			}
		}
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	writeLiveJSON(t, filepath.Join(root, "session.json"), loaded)
	entries, err := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	writeLiveJSON(t, filepath.Join(root, "tasks.json"), entries)
}

type codeLiveTransport struct {
	base  http.RoundTripper
	root  string
	mu    sync.Mutex
	calls int
}

func (w *codeLiveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.calls++
	id := w.calls
	w.mu.Unlock()
	start := time.Now()
	request := map[string]any{"method": r.Method, "path": r.URL.Path, "started": start}
	if r.GetBody != nil {
		b, _ := r.GetBody()
		if b != nil {
			raw, _ := io.ReadAll(b)
			b.Close()
			_ = os.WriteFile(filepath.Join(w.root, fmt.Sprintf("wire-%03d.request.json", id)), raw, 0600)
			var fields map[string]any
			if json.Unmarshal(raw, &fields) == nil {
				request["stream"] = fields["stream"]
				request["model"] = fields["model"]
			}
		}
	}
	response, err := w.base.RoundTrip(r)
	request["response_headers_ns"] = time.Since(start)
	if response != nil {
		request["status"] = response.StatusCode
		request["request_id"] = response.Header.Get("x-request-id")
	}
	if err != nil {
		request["error_code"] = errorcode.CodeOf(err)
	}
	raw, _ := json.Marshal(request)
	f, e := os.OpenFile(filepath.Join(w.root, "http.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e == nil {
		_, _ = f.Write(append(raw, '\n'))
		f.Close()
	}
	return response, err
}

type codeLiveTrace struct {
	model.LLM
	root  string
	mu    sync.Mutex
	calls int
}

func (m *codeLiveTrace) Capabilities() model.Capabilities {
	c, _ := model.CapabilitiesOf(m.LLM)
	return c
}
func (m *codeLiveTrace) Generate(ctx context.Context, r *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	m.calls++
	id := m.calls
	m.mu.Unlock()
	return func(yield func(*model.StreamEvent, error) bool) {
		if id > 32 {
			yield(nil, fmt.Errorf("focused acceptance generation cap reached"))
			return
		}
		if !r.Stream {
			yield(nil, fmt.Errorf("live acceptance requires stream"))
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 600*time.Second)
		defer cancel()
		attempts := 0
		ctx = model.WithInvocationAdmission(ctx, func(context.Context, *model.Request) error {
			attempts++
			if attempts > 1 {
				return fmt.Errorf("no automatic redispatch in live acceptance")
			}
			return nil
		})
		stem := filepath.Join(m.root, fmt.Sprintf("generation-%03d", id))
		raw, _ := json.MarshalIndent(r, "", "  ")
		_ = os.WriteFile(stem+".request.json", raw, 0600)
		prefix, _ := json.Marshal(struct {
			Instructions []model.Part     `json:"instructions"`
			Tools        []model.ToolSpec `json:"tools"`
		}{r.Instructions, r.Tools})
		_ = os.WriteFile(stem+".prefix.json", prefix, 0600)
		ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) {
			b, _ := json.MarshalIndent(in, "", "  ")
			_ = os.WriteFile(stem+".receipt.json", b, 0600)
		})
		first := true
		start := time.Now()
		for event, err := range m.LLM.Generate(ctx, r) {
			if event != nil && first {
				first = false
				b, _ := json.Marshal(map[string]any{"wall_ns": time.Since(start), "event": event})
				_ = os.WriteFile(stem+".first.json", b, 0600)
			}
			if event != nil && event.AttemptReset != nil {
				yield(nil, fmt.Errorf("stopped at requested retry; no repeated provider request"))
				return
			}
			if event != nil && event.Response != nil {
				b, _ := json.MarshalIndent(event.Response, "", "  ")
				_ = os.WriteFile(stem+".response.json", b, 0600)
			}
			if err != nil {
				b, _ := json.Marshal(map[string]any{"wall_ns": time.Since(start), "code": errorcode.CodeOf(err), "context_error": fmt.Sprint(ctx.Err())})
				_ = os.WriteFile(stem+".error.json", b, 0600)
			}
			if !yield(event, err) {
				return
			}
		}
	}
}
func selectedCodeModeModel(ctx context.Context, path, credentialRoot, selectedID string) (modelconfig.Config, func(), error) {
	noop := func() {}
	raw, err := os.ReadFile(path)
	if err != nil {
		return modelconfig.Config{}, noop, fmt.Errorf("authorized model config unavailable")
	}
	var doc struct {
		Models struct {
			Configs   []json.RawMessage `json:"configs"`
			Endpoints []json.RawMessage `json:"provider_endpoints"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return modelconfig.Config{}, noop, fmt.Errorf("invalid authorized config")
	}
	var cfg modelconfig.Config
	for _, row := range doc.Models.Configs {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(row, &id) == nil && id.ID == selectedID {
			if json.Unmarshal(row, &cfg) != nil {
				return cfg, noop, fmt.Errorf("invalid selected model")
			}
			break
		}
	}
	if cfg.ID != selectedID || cfg.Model != authorizedModelName(selectedID) || authorizedModelName(selectedID) == "" {
		return cfg, noop, fmt.Errorf("exact authorized model missing")
	}
	found := false
	for _, row := range doc.Models.Endpoints {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(row, &id) == nil && id.ID == cfg.ProviderEndpointID {
			var endpoint modelconfig.ProviderEndpointConfig
			if json.Unmarshal(row, &endpoint) != nil {
				return cfg, noop, fmt.Errorf("invalid selected endpoint")
			}
			cfg = modelconfig.MergeConfigProviderEndpoint(cfg, endpoint)
			found = true
			break
		}
	}
	if !found {
		return cfg, noop, fmt.Errorf("selected endpoint missing")
	}
	if cfg.CredentialRef == "" {
		return cfg, noop, nil
	}
	if !strings.HasPrefix(cfg.CredentialRef, "apikey:") || credentialRoot == "" {
		return cfg, noop, fmt.Errorf("single credential resolution requires explicit authorized API-key store root")
	}
	// Copy only the named record, never enumerate credentials or take a writable
	// lock in the source store. The canonical reader validates this private copy.
	ref := strings.ToLower(strings.TrimSpace(cfg.CredentialRef))
	sum := sha256.Sum256([]byte(ref))
	name := hex.EncodeToString(sum[:]) + ".json"
	record, err := os.ReadFile(filepath.Join(credentialRoot, "providers", "credentials", name))
	if err != nil {
		return cfg, noop, fmt.Errorf("selected credential unavailable")
	}
	temp, err := os.MkdirTemp("", "code-mode-model-credential-")
	if err != nil {
		return cfg, noop, fmt.Errorf("private credential directory unavailable")
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	dir := filepath.Join(temp, "providers", "credentials")
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(filepath.Join(dir, name), record, 0600) != nil {
		cleanup()
		return cfg, noop, fmt.Errorf("private credential copy failed")
	}
	store, err := credentialstore.New(temp)
	if err != nil {
		cleanup()
		return cfg, noop, fmt.Errorf("private credential resolver unavailable")
	}
	cfg.Token, err = store.Get(ctx, ref)
	if err != nil {
		cleanup()
		return cfg, noop, fmt.Errorf("selected credential resolution failed")
	}
	return cfg, cleanup, nil
}
