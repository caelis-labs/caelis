package gatewayapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// Opt-in native acceptance uses an authorized private account and a task-owned
// loopback page. Decisions come from the real provider, never a fixed model.
func TestBuiltinDesktopWorldLiveAgentTask(t *testing.T) {
	profilePath, artifacts := os.Getenv("CAELIS_MCP_LIVE_PROFILE"), os.Getenv("CAELIS_DTW_LIVE_ARTIFACTS")
	if profilePath == "" || artifacts == "" {
		t.Skip("requires a private model profile and native task artifacts directory")
	}
	if !filepath.IsAbs(artifacts) {
		t.Fatal("absolute private artifact directory required")
	}
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Models  []modelconfig.Config
		Secrets []string
	}
	raw, err := os.ReadFile(profilePath)
	if err != nil || json.Unmarshal(raw, &profile) != nil {
		t.Fatal("private model profile unavailable")
	}
	var cfg modelconfig.Config
	for _, candidate := range profile.Models {
		if candidate.Model == os.Getenv("CAELIS_MCP_LIVE_MODEL") {
			cfg = candidate
		}
	}
	if cfg.ID == "" {
		t.Fatal("exact authorized model absent")
	}
	redact := func(err error) string {
		s := fmt.Sprint(err)
		for _, secret := range profile.Secrets {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, "[REDACTED]")
			}
		}
		return s
	}
	nonce := make([]byte, 5)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	challenge := "CAELIS-" + hex.EncodeToString(nonce)
	title := "Caelis DTW Acceptance " + hex.EncodeToString(nonce)
	var receiptMu sync.Mutex
	var receipt []byte
	submissions := 0
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/submit" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			receiptMu.Lock()
			receipt = append([]byte(nil), body...)
			submissions++
			receiptMu.Unlock()
			_ = os.WriteFile(filepath.Join(artifacts, "page-receipt.txt"), body, 0o600)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<title>%s</title><h1>已记录</h1><p>提交值：%s</p>", title, body)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="zh"><meta charset="utf-8"><title>%s</title><body style="font:24px sans-serif;padding:48px"><h1>Caelis 本机验收记录</h1><p>待核对记录：清泉</p><p>请将以下完整校验值填入下方输入框，仅提交一次：</p><strong>%s</strong><form method="POST" action="/submit"><label for="answer">校验记录</label><input id="answer" name="answer" aria-label="校验记录" autocomplete="off"><button type="submit">提交验收记录</button></form><p>这是任务专用本机页面，不操作任何业务文件。</p></body></html>`, title, challenge)
	}))
	defer page.Close()
	if err := exec.Command("open", "-a", "Google Chrome", page.URL).Run(); err != nil {
		t.Fatal("cannot open task-owned browser page")
	}
	wire := &dtwLiveWire{base: &liveMCPWire{base: http.DefaultTransport}, root: artifacts}
	host := startApplicationHTTPHost(t, filepath.Join(t.TempDir(), "store"), t.TempDir(), wire)
	defer host.close(t)
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Minute)
	defer cancel()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(redact(err))
	}
	result, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "live-model-connect", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "custom-responses", AuthType: string(cfg.AuthType), Model: cfg.Model, BaseURL: cfg.BaseURL, APIKey: cfg.Token, ImageInput: cfg.ImageInput, ContextWindowTokens: cfg.ContextWindowTokens, MaxOutputTokens: 12000, TimeoutSeconds: 90}})
	writeLiveJSON(t, filepath.Join(artifacts, "model-connect-receipt.json"), result)
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		writeLiveJSON(t, filepath.Join(artifacts, "model-connect-error.json"), map[string]any{"error": redact(err), "expected": status.Configuration.Revision})
		t.Fatal("private account connection failed: " + redact(err))
	}
	p := applicationHTTPProfile()
	p.Model = "custom/" + cfg.Model
	p.Tools = nil
	p.Plugins = []sessionprofile.Plugin{}
	if root := os.Getenv("CAELIS_DTW_LIVE_PLUGIN"); root != "" {
		p.Plugins = []sessionprofile.Plugin{{Root: root}}
	}
	p.Instructions = "你是 Caelis Agent。用 Skill、tool_search、mcp_call 完成用户的本机桌面验收；Read 仅可读取该 desktop-world Skill 目录内的随包参考文档。先自主读取 desktop-world Skill，然后自主发现真实工具。只操作本任务标题的 Chrome 页面，不操作其他标签页、用户文件或外部服务。不要用文件读取/终端/HTTP接口绕过桌面读取验收数据。未知作用不重放，使用只读观察核对已发生的效果。"
	creation := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "live-task-create"}, Profile: &p}
	if os.Getenv("CAELIS_DTW_LIVE_SESSION") == "ordinary" {
		creation.Profile = nil
		creation.Model = p.Model
	}
	created, err := host.host.CreateSession(ctx, creation)
	if err != nil {
		t.Fatal(redact(err))
	}
	defer func() {
		evidenceCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if original, err := host.stack.Sessions().LoadSession(evidenceCtx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: created.SessionID}}); err == nil {
			writeLiveJSON(t, filepath.Join(artifacts, "session-on-close.json"), original)
		}
		if receipt, err := host.host.Operation(evidenceCtx, "live-task-prompt"); err == nil {
			writeLiveJSON(t, filepath.Join(artifacts, "prompt-receipt.json"), receipt)
		}
	}()
	if creation.Profile == nil {
		state, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
		if err != nil {
			t.Fatal(redact(err))
		}
		if _, err := host.host.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "live-manual", ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Mode: "manual"}); err != nil {
			t.Fatal(redact(err))
		}
	}
	prompt := p.Instructions + "\n" + fmt.Sprintf("请用 DTW 完成真实桌面任务：找到 Google Chrome 中标题为 %q 的本机页面；读取记录名称和完整校验值，定位校验记录输入框并截图，然后将页面上看到的完整值填写进去，仅提交一次。提交后用只读观察确认。最后报告读取到的记录名称、值及观察依据；不能凭任务文本猜值。页面地址仅作定位参考：%s。", title, page.URL)
	if _, err := host.host.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "live-task-prompt"}, Input: prompt, SourceKind: "user"}); err != nil {
		t.Fatal(redact(err))
	}
	approvals := 0
	selectedRoot := os.Getenv("CAELIS_DTW_LIVE_PLUGIN")
	for {
		state, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
		if err != nil {
			t.Fatal(redact(err))
		}
		if active := state.Approval.Active; active != nil {
			selected, err := host.host.SessionMCPStatus(ctx, created.SessionID)
			if err != nil || len(selected.Plugins) != 1 || selected.Plugins[0].ID != "desktop-world" || selected.Plugins[0].Version != "0.3.4" || len(selected.Servers) != 1 || len(selected.Servers[0].Tools) != 2 || len(selected.Skills) != 1 {
				t.Fatal("live source does not contain one paired 0.3.4 MCP/Skill package")
			}
			if root := os.Getenv("CAELIS_DTW_LIVE_PLUGIN"); root != "" && selected.Plugins[0].Root != root {
				t.Fatal("explicit package silently fell back to another source")
			}
			writeLiveJSON(t, filepath.Join(artifacts, "selected-source.json"), selected)
			selectedRoot = selected.Plugins[0].Root
			writeLiveJSON(t, filepath.Join(artifacts, fmt.Sprintf("approval-%d.json", approvals)), active)
			encoded, _ := json.Marshal(active.Permission)
			if active.Permission == nil || active.Permission.ToolCall.Name != "desktop_world__exec" || active.Permission.ToolCall.RawInput["app"] != "com.google.Chrome" || active.Permission.ToolCall.RawInput["source"] == nil || !bytes.Contains(encoded, []byte("com.google.Chrome")) {
				t.Fatal("refusing non-target app or non-DTW approval")
			}
			pending, err := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: created.SessionID}})
			if err != nil {
				t.Fatal(redact(err))
			}
			matched := false
			for _, event := range pending.Events {
				if event.Type == session.EventTypeToolCall && event.Tool != nil && event.Tool.ID == active.Permission.ToolCall.ID {
					matched = event.Tool.Name == active.Permission.ToolCall.Name && event.Tool.Input["app"] == active.Permission.ToolCall.RawInput["app"] && event.Tool.Input["source"] == active.Permission.ToolCall.RawInput["source"] && event.Tool.ModelCall != nil && event.Tool.ModelCall.ID == event.Tool.ID
				}
			}
			if !matched {
				t.Fatal("approval does not match the original canonical call and model envelope")
			}
			approvals++
			if _, err := host.host.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: fmt.Sprintf("live-approve-%d", approvals)}, Target: active.Target, ApprovalRequestID: string(active.RequestID), Outcome: "selected", OptionID: "allow_session", Approved: true}); err != nil {
				t.Fatal(redact(err))
			}
		}
		if !state.Run.Active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native task deadline; original operation retained, never replay")
		case <-time.After(150 * time.Millisecond):
		}
	}
	loaded, err := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: created.SessionID}})
	if err != nil {
		t.Fatal(redact(err))
	}
	writeLiveJSON(t, filepath.Join(artifacts, "session.json"), loaded)
	receiptMu.Lock()
	final := append([]byte(nil), receipt...)
	submitted := submissions
	receiptMu.Unlock()
	values, _ := url.ParseQuery(string(final))
	writeLiveJSON(t, filepath.Join(artifacts, "page-receipt.json"), map[string]any{"page_title": title, "expected_answer": challenge, "received_form": string(final), "page_submissions": submitted})
	counts := map[string]int{}
	for _, e := range loaded.Events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil {
			counts[e.Tool.Name]++
			skillReference := false
			if e.Tool.Name == "Read" && selectedRoot != "" {
				path, _ := e.Tool.Input["path"].(string)
				skillReference = filepath.IsAbs(path) && plugin.PathWithinRoot(filepath.Join(selectedRoot, "skills", "desktop-world"), path)
			}
			if e.Tool.Name != "Skill" && e.Tool.Name != "tool_search" && !strings.HasPrefix(e.Tool.Name, "desktop_world__") && !skillReference {
				t.Fatal("Agent bypassed the authorized desktop-only task path")
			}
			if strings.HasPrefix(e.Tool.Name, "desktop_world__") && (e.Tool.ModelCall == nil || e.Tool.ModelCall.Name != "mcp_call" || e.Tool.ModelCall.ID != e.Tool.ID) {
				t.Fatal("real MCP result lost its original model envelope")
			}
		}
	}
	wire.base.mu.Lock()
	hashes := append([]string(nil), wire.base.hashes...)
	models := wire.base.models
	wire.base.mu.Unlock()
	writeLiveJSON(t, filepath.Join(artifacts, "summary.json"), map[string]any{"model": cfg.Model, "session_id": created.SessionID, "app_approvals": approvals, "tool_results": counts, "tools_hashes": hashes, "wire_models": models, "task_receipt_matches": values.Get("answer") == challenge, "page_submissions": submitted, "session_kind": os.Getenv("CAELIS_DTW_LIVE_SESSION"), "plugin_override": os.Getenv("CAELIS_DTW_LIVE_PLUGIN"), "page_title": title})
	if approvals != 1 || submitted != 1 || values.Get("answer") != challenge || counts["Skill"] == 0 || counts["desktop_world__apps"] == 0 || counts["desktop_world__exec"] == 0 || counts["desktop_world__api"] != 0 || counts["desktop_world__result"] != 0 {
		t.Fatal("real Agent task acceptance incomplete; inspect private original evidence")
	}
	for _, h := range hashes {
		if h != hashes[0] {
			t.Fatal("main model tools prefix changed during discovery")
		}
	}
	t.Logf("real model=%s target_app=com.google.Chrome approvals=%d independent_task_receipt=verified tool_result_counts=%v main_requests=%d", cfg.Model, approvals, counts, len(hashes))
}

type dtwLiveWire struct {
	base  *liveMCPWire
	root  string
	mu    sync.Mutex
	count int
}

func (w *dtwLiveWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.count++
	n := w.count
	w.mu.Unlock()
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		_ = os.WriteFile(filepath.Join(w.root, fmt.Sprintf("wire-request-%03d.json", n)), b, 0o600)
	}
	response, err := w.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(w.root, fmt.Sprintf("wire-response-%03d.sse", n)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	response.Body = &dtwTeeBody{Reader: io.TeeReader(response.Body, file), body: response.Body, file: file}
	return response, nil
}

type dtwTeeBody struct {
	io.Reader
	body io.Closer
	file *os.File
}

func (b *dtwTeeBody) Close() error { err := b.body.Close(); _ = b.file.Close(); return err }
