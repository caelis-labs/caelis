package gatewayapp_test

import (
	"bytes"
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
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// Live acceptance consumes only an explicitly provided private profile. It never
// reads or edits production configuration. Its policy allows only these exact
// read operations, including when a model proposes a different tool.
func TestMCPFixedEntryLive(t *testing.T) {
	profilePath := os.Getenv("CAELIS_MCP_LIVE_PROFILE")
	if profilePath == "" {
		t.Skip("requires an authorized private MCP/model profile")
	}
	var profile struct {
		Models  []modelconfig.Config
		Servers []mcp.ServerSpec
		Secrets []string
	}
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal("private profile unavailable")
	}
	if json.Unmarshal(raw, &profile) != nil {
		t.Fatal("invalid private profile")
	}
	redact := func(err error) string {
		if err == nil {
			return ""
		}
		s := err.Error()
		for _, secret := range profile.Secrets {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
		return s
	}
	selected := os.Getenv("CAELIS_MCP_LIVE_MODEL")
	var cfg modelconfig.Config
	for _, c := range profile.Models {
		if c.Model == selected {
			cfg = c
		}
	}
	if cfg.ID == "" {
		t.Fatal("exact configured model missing from private profile")
	}
	root := os.Getenv("CAELIS_MCP_LIVE_ARTIFACTS")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute private artifact directory required")
	}
	if os.MkdirAll(root, 0700) != nil {
		t.Fatal("artifact directory unavailable")
	}
	wire := &liveMCPWire{base: http.DefaultTransport}
	cfg.HTTPClient = &http.Client{Transport: wire, Timeout: 5 * time.Minute}
	resolved, err := modelconfig.BuildModel(cfg, 0, 0)
	if err != nil {
		t.Fatal(redact(err))
	}
	trace := &liveMCPTrace{LLM: resolved.Model, root: root}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	manager, err := mcp.NewManager(ctx, profile.Servers, func(f mcp.ServerFailure) { t.Logf("connection failure server=%s error=%s", f.Name, redact(f.Err)) })
	if err != nil {
		t.Fatal(redact(err))
	}
	defer manager.Close()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tools := manager.SearchTools()
	catalog := map[string]int{}
	for _, one := range tools {
		d := one.Definition()
		server, _ := d.Metadata[tool.MetadataMCPServer].(string)
		catalog[server]++
	}
	for _, name := range []string{"context7", "tencent-docs", "amap"} {
		if catalog[name] == 0 {
			t.Fatalf("live MCP %s unavailable; catalog=%v", name, catalog)
		}
	}
	writeLiveJSON(t, filepath.Join(root, "catalog.json"), catalog)
	allowed := map[string]bool{"resolve-library-id": true, "query-docs": true, "get-library-docs": true, "manage.search_file": true, "manage.folder_list": true, "maps_text_search": true, "maps_search_detail": true, "maps_geo": true, "maps_direction_driving": true, "maps_direction_walking": true, "maps_direction_transit_integrated": true}
	registry := liveMCPPolicy{allowed: allowed}
	grants, err := runtime.NewMCPGrantStore(filepath.Join(root, "mcp-grants.json"))
	if err != nil {
		t.Fatal(redact(err))
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "mcp-live", Workspace: session.WorkspaceRef{Key: "isolated-acceptance", CWD: profile.Servers[0].WorkDir}})
	if err != nil {
		t.Fatal(redact(err))
	}
	approvals := 0
	requester := liveApprovalFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		remote, _ := r.Tool.Metadata[tool.MetadataMCPTool].(string)
		if !allowed[remote] || r.Call.Name != r.Tool.Name || r.Call.ModelCall == nil {
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
		}
		approvals++
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	rt, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "live-read-only", MCPGrants: grants})
	if err != nil {
		t.Fatal(redact(err))
	}
	prompts := []string{
		"只读验收。通过 context7 查询 FastAPI BackgroundTasks 的用法，先解析库身份，再检索 API 文档，给出 add_task 的参数与一个最短例子并附来源。需要实际调用外部 MCP，不能凭记忆回答。",
		"继续只读验收。通过高德地图 MCP 查询北京南站和天安门的地点坐标，再查询两地驾车路线，报告两地名称、坐标、路线距离与耗时。需要实际外部结果，明确地点歧义，不能猜测。",
		"继续只读验收。先用 tool_search 搜索腾讯文档 manage.search_file（只读标题搜索能力），检索成功后才可 mcp_call。通过此能力搜索标题关键词 MCP，最多列出三个匹配项的标题和类型；若无匹配如实报告。只查询，不创建、修改、删除或分享任何既有文档。",
	}
	for i, prompt := range prompts {
		result, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: prompt, ApprovalRequester: requester, AgentSpec: agent.AgentSpec{Model: trace, Tools: []tool.Tool{toolsearch.NewSource(manager)}, DeferredTools: manager}})
		if err != nil {
			t.Fatal(redact(err))
		}
		if err := result.Handle.WaitCompletion(ctx); err != nil {
			t.Fatal(redact(err))
		}
		t.Logf("completed live turn=%d", i+1)
	}
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(redact(err))
	}
	searches, calls, failures := 0, 0, 0
	used := map[string]int{}
	for _, e := range loaded.Events {
		if e.Tool == nil || e.Type != session.EventTypeToolResult {
			continue
		}
		if e.Tool.Name == tool.ToolSearchToolName {
			searches++
		}
		if e.Tool.ModelCall != nil {
			calls++
			if e.Tool.Status == "failed" {
				failures++
			}
			for _, one := range tools {
				d := one.Definition()
				if d.Name == e.Tool.Name {
					s, _ := d.Metadata[tool.MetadataMCPServer].(string)
					used[s]++
				}
			}
			if e.Tool.Name == tool.MCPCallToolName || e.Tool.ID != e.Tool.ModelCall.ID {
				t.Fatal("real target presentation or outer pairing lost")
			}
		}
	}
	wire.mu.Lock()
	hashes := append([]string(nil), wire.hashes...)
	wire.mu.Unlock()
	writeLiveJSON(t, filepath.Join(root, "wire-tools.json"), hashes)
	receipt := map[string]any{"configured_provider": cfg.Provider, "configured_model": cfg.Model, "model_id": cfg.ID, "searches": searches, "calls": calls, "tool_failures": failures, "approvals": approvals, "used_sources": used, "main_wire_requests": len(hashes), "session_id": active.SessionID}
	writeLiveJSON(t, filepath.Join(root, "receipt.json"), receipt)
	t.Logf("live receipt: model=%s searches=%d calls=%d failures=%d approvals=%d sources=%v wire_requests=%d", cfg.Model, searches, calls, failures, approvals, used, len(hashes))
	if len(hashes) < 2 {
		t.Fatal("insufficient actual serialized main requests")
	}
	for _, h := range hashes {
		if h != hashes[0] {
			t.Fatal("actual main provider tools bytes changed")
		}
	}
	for _, name := range []string{"context7", "tencent-docs", "amap"} {
		if used[name] == 0 {
			t.Errorf("model did not invoke source %s", name)
		}
	}
	if searches < 3 || calls < 4 || failures > 0 {
		t.Errorf("incomplete live tasks: searches=%d calls=%d failures=%d", searches, calls, failures)
	}
}

type liveMCPPolicy struct{ allowed map[string]bool }

func (p liveMCPPolicy) Lookup(_ context.Context, id string) (policy.Mode, bool, error) {
	if id != "live-read-only" {
		return nil, false, nil
	}
	return policy.NamedMode{ID: id, Decide: func(_ context.Context, r policy.ToolContext) (policy.Decision, error) {
		if tool.IsMCPDefinition(r.Tool) {
			name, _ := r.Tool.Metadata[tool.MetadataMCPTool].(string)
			if !p.allowed[name] {
				return policy.Decision{Action: policy.ActionDeny, Reason: "outside authorized read-only acceptance operations"}, nil
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}, true, nil
}

type liveMCPWire struct {
	base   http.RoundTripper
	mu     sync.Mutex
	hashes []string
}

func (w *liveMCPWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil {
			raw := fields["tools"]
			if bytes.Contains(raw, []byte(`"mcp_call"`)) {
				sum := sha256.Sum256(raw)
				w.mu.Lock()
				w.hashes = append(w.hashes, hex.EncodeToString(sum[:]))
				w.mu.Unlock()
			}
		}
	}
	return w.base.RoundTrip(r)
}

type liveMCPTrace struct {
	model.LLM
	root  string
	mu    sync.Mutex
	calls int
}

func (m *liveMCPTrace) Capabilities() model.Capabilities {
	caps, _ := model.CapabilitiesOf(m.LLM)
	return caps
}
func (m *liveMCPTrace) Generate(ctx context.Context, r *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	m.calls++
	id := m.calls
	m.mu.Unlock()
	raw, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(m.root, fmt.Sprintf("model-request-%03d.json", id)), raw, 0600)
	return func(yield func(*model.StreamEvent, error) bool) {
		f, err := os.OpenFile(filepath.Join(m.root, fmt.Sprintf("model-stream-%03d.jsonl", id)), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			yield(nil, err)
			return
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for e, err := range m.LLM.Generate(ctx, r) {
			if e != nil {
				_ = enc.Encode(e)
			}
			if err != nil {
				_ = enc.Encode(map[string]any{"error": err.Error()})
			}
			if !yield(e, err) {
				return
			}
		}
	}
}
func writeLiveJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("private evidence write failed")
	}
}

type liveApprovalFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f liveApprovalFunc) RequestApproval(ctx context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return f(ctx, r)
}

func (m *liveMCPTrace) ContextWindowTokens() int {
	if source, ok := m.LLM.(interface{ ContextWindowTokens() int }); ok {
		return source.ContextWindowTokens()
	}
	return 0
}
