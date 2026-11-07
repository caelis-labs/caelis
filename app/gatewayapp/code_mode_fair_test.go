package gatewayapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
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

const fairProductionBase = "6d15d1e6078b1b20d2a27f219331c3a00f0e29ad"
const fairQuestion = "Goja Runtime.Interrupt: may it be called from a goroutine other than the VM owner?"
const fairSystem = "Complete the task using actual tools and only this isolated workspace. No shell/process execution or external writes. Do not invent documentation or replay unknown effects."

type fairTurn struct {
	Label  string            `json:"label"`
	Common string            `json:"common"`
	Inputs map[string]string `json:"inputs"`
	Total  int               `json:"expected_total"`
}
type fairSession struct {
	ID    string `json:"id"`
	Pair  int    `json:"pair"`
	Route string `json:"route"`
}
type fairManifest struct {
	Version        string              `json:"version"`
	ProductionBase string              `json:"production_base"`
	HarnessSHA256  string              `json:"harness_sha256"`
	Model          map[string]any      `json:"model"`
	Context7       map[string]any      `json:"context7"`
	System         string              `json:"system"`
	Question       string              `json:"question"`
	Routes         map[string]string   `json:"entry_guidance"`
	Turns          []fairTurn          `json:"turns"`
	Prompts        map[string][]string `json:"exact_prompts"`
	Schedule       []fairSession       `json:"schedule"`
	Controls       map[string]any      `json:"controls"`
	Assertions     []string            `json:"assertions"`
}

func newFairManifest(parameters, context7 map[string]any, harness string) fairManifest {
	routes := map[string]string{
		"native": "Use native filesystem tools and the existing ToolSearch/mcp_call entries for Context7.",
		"js":     "Use js_exec inline code with tools.search/describe/call for Context7 and filesystem orchestration; native filesystem tools remain available.",
	}
	common := `Shell/process execution is forbidden for this task, including RunCommand, aliases and equivalent process tools. Work only in this workspace. Read a.txt and b.txt using actual tools and sum their integers. Read returns JSON content with numbered lines; parse the values, not the line numbers.
Use Context7 read-only documentation to answer this question: ` + fairQuestion + `
Resolve Goja's library ID, then query its docs with that exact question text in this Turn. Keep the actual returned source/library ID; if relevant evidence is unavailable, explicitly report unavailable instead of guessing.
Write answer.json with exactly four fields: total (integer), api_status (found or unavailable), api_answer (one short sentence, at most 300 characters), api_source (one actual library ID or URL, or empty when unavailable). Finish with a concise total/API/source answer. Select only relevant documentation output. Do not create or execute workflow.js or any other saved script; JS may use inline code only. Do not edit a.txt or b.txt. No other files or external services may be written.`
	turns := []fairTurn{
		{Label: "first", Common: "Inputs are a.txt=17 and b.txt=25.\n" + common, Inputs: map[string]string{"a.txt": "17\n", "b.txt": "25\n"}, Total: 42},
		{Label: "continuation", Common: "Continue in this same Session. The harness has updated a.txt to 18; b.txt remains 25. Re-read both files and replace answer.json.\n" + common, Inputs: map[string]string{"a.txt": "18\n", "b.txt": "25\n"}, Total: 43},
	}
	prompts := map[string][]string{}
	for route, entry := range routes {
		for _, turn := range turns {
			prompts[route] = append(prompts[route], entry+"\n\n"+turn.Common)
		}
	}
	return fairManifest{Version: "fair-a-no-gui-v1", ProductionBase: fairProductionBase, HarnessSHA256: harness, Model: parameters, Context7: context7, System: fairSystem, Question: fairQuestion, Routes: routes, Turns: turns, Prompts: prompts,
		Schedule:   []fairSession{{"pair1-native", 1, "native"}, {"pair1-js", 1, "js"}, {"pair2-js", 2, "js"}, {"pair2-native", 2, "native"}},
		Controls:   map[string]any{"sessions": 4, "turns_per_session": 2, "pairs": 2, "gui": false, "stream": true, "per_generation_seconds": 600, "max_generations_per_turn_including_selectors": 24, "http_client_timeout_seconds": 0, "response_header_timeout_seconds": 0, "automatic_retries": 0, "script_file_obligation": "none for either route; file capability B uses historical evidence only", "answer_file": "retained between Turns; revalidated after each", "stop_policy": "Stop entire study on any model/runtime error, incomplete generation, unknown tool outcome, or instrumentation failure; no replacement sampling. Record assertion failures and continue fixed scheduled Turns without retry.", "cache_accounting": "Within each independent Session only; first/continuation and each pair separate. Provider reported prompt/cached/fresh/completion; no inference of KV caching from common HTTP body bytes."},
		Assertions: []string{"both input files actually read per Turn; exact expected total", "answer.json four-field schema; short API answer and real returned source", "successful Context7 docs query with frozen question each Turn; relevant API support reviewed against original result", "no shell/process dispatch; no GUI; no saved script generation or execution", "same Session for its two Turns; four independent workspaces/Sessions", "serialized main-request instructions/tools byte-equal within each Session", "preserve original request/stream/Response/usage/invocation and Tool receipts, including failures and missing usage"}}
}

func fairHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func fairJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
func fairModelParameters(cfg modelconfig.Config) map[string]any {
	cfg = modelconfig.NormalizeConfig(cfg)
	u, _ := url.Parse(cfg.BaseURL)
	return map[string]any{"id": cfg.ID, "model": cfg.Model, "provider": cfg.Provider, "api": cfg.API, "endpoint_id": cfg.ProviderEndpointID, "endpoint_origin": u.Scheme + "://" + u.Host, "reasoning_effort": cfg.ReasoningEffort, "default_reasoning_effort": cfg.DefaultReasoningEffort, "reasoning_mode": cfg.ReasoningMode, "max_output_tokens": cfg.MaxOutputTok, "context_window_tokens": cfg.ContextWindowTokens, "configured_timeout_ns": int64(cfg.Timeout), "configured_first_event_timeout_ns": int64(cfg.StreamFirstEventTimeout), "stream": true, "generation_budget_seconds": 600}
}

// No model call or MCP startup occurs during preparation. The model resolver
// reads only the previously authorized selected config/endpoint/credential.
func TestCodeModeFairPrepare(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_FAIR_PREPARE")
	if root == "" {
		t.Skip("explicit task-owned preparation directory required")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute artifact directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest already exists; do not overwrite frozen study")
	}
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if (cfg.Timeout > 0 && cfg.Timeout < 600*time.Second) || (cfg.StreamFirstEventTimeout > 0 && cfg.StreamFirstEventTimeout < 600*time.Second) {
		t.Fatal("selected profile has a shorter model budget; coordinate instead of silently overriding it")
	}
	entry := os.Getenv("CAELIS_CODE_MODE_LIVE_CONTEXT7_ENTRY")
	raw, err := os.ReadFile(entry)
	if err != nil || !filepath.IsAbs(entry) {
		t.Fatal("existing explicit Context7 entry required")
	}
	packageBytes, err := os.ReadFile(filepath.Join(filepath.Dir(entry), "..", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(packageBytes, &pkg) != nil || pkg.Name != "@upstash/context7-mcp" {
		t.Fatal("unexpected Context7 module")
	}
	source, err := os.ReadFile("code_mode_fair_test.go")
	if err != nil {
		t.Fatal(err)
	}
	manifest := newFairManifest(fairModelParameters(cfg), map[string]any{"entry": entry, "entry_sha256": fairHash(raw), "package": pkg.Name, "version": pkg.Version, "package_json_sha256": fairHash(packageBytes), "configuration": "reuse only existing context7 entry arguments/environment; no installer, credentials omitted"}, fairHash(source))
	writeLiveJSON(t, filepath.Join(root, "manifest.json"), manifest)
	frozen, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "manifest.sha256"), []byte(fairHash(frozen)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("prepared without network: manifest_sha256=%s", fairHash(frozen))
}

func TestCodeModeFairPlanOffline(t *testing.T) {
	m := newFairManifest(nil, nil, "")
	want := []fairSession{{"pair1-native", 1, "native"}, {"pair1-js", 1, "js"}, {"pair2-js", 2, "js"}, {"pair2-native", 2, "native"}}
	if !reflect.DeepEqual(m.Schedule, want) || len(m.Turns) != 2 || m.Turns[0].Total != 42 || m.Turns[1].Total != 43 || m.Turns[1].Inputs["a.txt"] != "18\n" || m.Controls["gui"] != false {
		t.Fatal("frozen order/continuation inputs changed")
	}
	for i, turn := range m.Turns {
		for route, entry := range m.Routes {
			if got := strings.TrimPrefix(m.Prompts[route][i], entry+"\n\n"); got != turn.Common {
				t.Fatalf("route %s has extra task work", route)
			}
		}
		if !strings.Contains(turn.Common, "Shell/process execution is forbidden") || !strings.Contains(turn.Common, "Do not create or execute workflow.js") || !strings.Contains(turn.Common, fairQuestion) {
			t.Fatal("unequal shell/file/API contract")
		}
	}
}

type fairContextKey struct{}
type fairRequestContext struct {
	Turn       int
	Generation int
	Kind       string
}

type fairTransport struct {
	base  http.RoundTripper
	root  string
	mu    sync.Mutex
	calls int
}

func (w *fairTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.calls++
	id := w.calls
	w.mu.Unlock()
	link, _ := r.Context().Value(fairContextKey{}).(fairRequestContext)
	stem := filepath.Join(w.root, fmt.Sprintf("wire-%03d", id))
	start := time.Now()
	if r.GetBody == nil {
		return nil, fmt.Errorf("cannot retain original HTTP request bytes")
	}
	body, err := r.GetBody()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(stem+".request.json", raw, 0600); err != nil {
		return nil, err
	}
	var requestValues map[string]json.RawMessage
	if json.Unmarshal(raw, &requestValues) != nil {
		return nil, fmt.Errorf("invalid retained HTTP request")
	}
	wirePrefix := fairJSON(map[string]json.RawMessage{"instructions": requestValues["instructions"], "tools": requestValues["tools"]})
	if err = os.WriteFile(stem+".instructions-tools.json", wirePrefix, 0600); err != nil {
		return nil, err
	}
	meta := map[string]any{"method": r.Method, "path": r.URL.Path, "started": start, "turn": link.Turn, "generation": link.Generation, "kind": link.Kind, "request_bytes": len(raw), "instructions_tools_value_sha256": fairHash(wirePrefix), "instructions_tools_value_bytes": len(wirePrefix)}
	resp, callErr := w.base.RoundTrip(r)
	meta["response_headers_ns"] = time.Since(start)
	if resp != nil {
		meta["status"] = resp.StatusCode
		meta["request_id"] = resp.Header.Get("x-request-id")
		file, e := os.OpenFile(stem+".response.raw", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			_ = resp.Body.Close()
			return nil, e
		}
		resp.Body = &fairResponseBody{ReadCloser: resp.Body, file: file}
	}
	if callErr != nil {
		meta["error_code"] = errorcode.CodeOf(callErr)
		meta["error_type"] = fmt.Sprintf("%T", callErr)
	}
	if err = os.WriteFile(stem+".meta.json", fairJSON(meta), 0600); err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return resp, callErr
}

type fairResponseBody struct {
	io.ReadCloser
	file *os.File
}

func (b *fairResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, writeErr := b.file.Write(p[:n])
		err = errors.Join(err, writeErr)
	}
	return n, err
}
func (b *fairResponseBody) Close() error { return errors.Join(b.ReadCloser.Close(), b.file.Close()) }

type fairTrace struct {
	model.LLM
	root      string
	mu        sync.Mutex
	calls     int
	turnCalls map[int]int
	prefix    []byte
}

func (m *fairTrace) Capabilities() model.Capabilities { c, _ := model.CapabilitiesOf(m.LLM); return c }
func (m *fairTrace) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		link, _ := ctx.Value(fairContextKey{}).(fairRequestContext)
		m.mu.Lock()
		m.calls++
		id := m.calls
		m.turnCalls[link.Turn]++
		count := m.turnCalls[link.Turn]
		m.mu.Unlock()
		if count > 24 || !req.Stream {
			yield(nil, fmt.Errorf("frozen stream/generation boundary reached"))
			return
		}
		kind := "main"
		if len(req.Tools) == 1 && req.Tools[0].Function != nil && req.Tools[0].Function.Name == "InspectToolSchema" {
			kind = "selector"
		}
		link.Generation = id
		link.Kind = kind
		ctx = context.WithValue(ctx, fairContextKey{}, link)
		ctx, cancel := context.WithTimeout(ctx, 600*time.Second)
		defer cancel()
		stem := filepath.Join(m.root, fmt.Sprintf("generation-%03d", id))
		save := func(suffix string, value any) error { return os.WriteFile(stem+suffix, fairJSON(value), 0600) }
		if err := save(".request.json", req); err != nil {
			yield(nil, err)
			return
		}
		prefix := fairJSON(struct {
			Instructions []model.Part     `json:"instructions"`
			Tools        []model.ToolSpec `json:"tools"`
		}{req.Instructions, req.Tools})
		stable := true
		if kind == "main" {
			m.mu.Lock()
			if m.prefix == nil {
				m.prefix = prefix
			} else {
				stable = bytes.Equal(m.prefix, prefix)
			}
			m.mu.Unlock()
		}
		if err := os.WriteFile(stem+".prefix.json", prefix, 0600); err != nil {
			yield(nil, err)
			return
		}
		if err := save(".meta.json", map[string]any{"turn": link.Turn, "kind": kind, "generation": id, "prefix_sha256": fairHash(prefix), "prefix_bytes": len(prefix), "session_main_prefix_equal": stable, "stream": req.Stream, "budget_seconds": 600}); err != nil {
			yield(nil, err)
			return
		}
		if !stable {
			yield(nil, fmt.Errorf("Session instructions/tools changed; stopping frozen study"))
			return
		}
		attempts := 0
		ctx = model.WithInvocationAdmission(ctx, func(context.Context, *model.Request) error {
			attempts++
			if attempts > 1 {
				return fmt.Errorf("frozen study forbids provider redispatch")
			}
			return nil
		})
		var captureErr error
		ctx = model.WithInvocationStart(ctx, func(_ context.Context, in model.Invocation, _ *model.Request) error { return save(".start.json", in) })
		ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) {
			captureErr = errors.Join(captureErr, save(".receipt.json", in))
			if in.Outcome != "completed" {
				cancel()
			}
		})
		streamFile, err := os.OpenFile(stem+".stream.jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			yield(nil, err)
			return
		}
		defer streamFile.Close()
		first, complete := true, false
		start := time.Now()
		for event, err := range m.LLM.Generate(ctx, req) {
			if event != nil {
				if _, e := streamFile.Write(append(fairJSON(event), '\n')); e != nil {
					yield(nil, e)
					return
				}
				if first {
					first = false
					if e := save(".first.json", map[string]any{"wall_ns": time.Since(start), "event": event}); e != nil {
						yield(nil, e)
						return
					}
				}
				if event.AttemptReset != nil {
					yield(nil, fmt.Errorf("retry requested; frozen study stops without redispatch"))
					return
				}
				if event.Response != nil {
					if e := save(".response.json", event.Response); e != nil {
						yield(nil, e)
						return
					}
					complete = event.TurnComplete
				}
			}
			if err != nil {
				_ = save(".error.json", map[string]any{"wall_ns": time.Since(start), "code": errorcode.CodeOf(err), "type": fmt.Sprintf("%T", err), "context_error": fmt.Sprint(ctx.Err())})
				yield(event, err)
				return
			}
			if !yield(event, nil) {
				return
			}
		}
		if captureErr != nil {
			yield(nil, captureErr)
			return
		}
		if !complete {
			yield(nil, fmt.Errorf("incomplete generation; original stream retained, no retry"))
		}
	}
}

func fairContext7Spec(configPath, entry, workspace string) (mcp.ServerSpec, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return mcp.ServerSpec{}, fmt.Errorf("selected Context7 config unavailable")
	}
	var doc struct {
		Servers map[string]struct {
			Args []string          `json:"args"`
			Env  map[string]string `json:"env"`
		} `json:"mcp_servers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return mcp.ServerSpec{}, fmt.Errorf("selected Context7 config invalid")
	}
	server, ok := doc.Servers["context7"]
	if !ok {
		return mcp.ServerSpec{}, fmt.Errorf("exact Context7 config missing")
	}
	args := []string{entry}
	found := false
	for _, arg := range server.Args {
		if arg == "-y" || arg == "--yes" {
			continue
		}
		if strings.HasPrefix(arg, "@upstash/context7-mcp") {
			found = true
			continue
		}
		args = append(args, arg)
	}
	if !found {
		return mcp.ServerSpec{}, fmt.Errorf("unexpected Context7 launch configuration")
	}
	return mcp.ServerSpec{PluginID: "context7", Name: "context7", Transport: mcp.TransportStdio, Command: "node", Args: args, Env: server.Env, WorkDir: workspace, CleanEnvironment: true}, nil
}

func TestCodeModeFairLive(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_FAIR_RUN")
	if root == "" {
		t.Skip("requires frozen authorized fair study")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fairHash(raw) != os.Getenv("CAELIS_CODE_MODE_FAIR_MANIFEST_SHA256") {
		t.Fatal("explicit frozen manifest hash mismatch")
	}
	var manifest fairManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.ProductionBase != fairProductionBase {
		t.Fatal("invalid frozen study")
	}
	source, err := os.ReadFile("code_mode_fair_test.go")
	if err != nil || fairHash(source) != manifest.HarnessSHA256 {
		t.Fatal("harness changed after freeze")
	}
	if !bytes.Equal(fairJSON(manifest), fairJSON(newFairManifest(manifest.Model, manifest.Context7, manifest.HarnessSHA256))) {
		t.Fatal("manifest contract mismatch")
	}
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !bytes.Equal(fairJSON(fairModelParameters(cfg)), fairJSON(manifest.Model)) {
		t.Fatal("selected model parameters changed after freeze")
	}
	entry, _ := manifest.Context7["entry"].(string)
	entryBytes, err := os.ReadFile(entry)
	if err != nil || fairHash(entryBytes) != manifest.Context7["entry_sha256"] {
		t.Fatal("Context7 entry changed after freeze")
	}
	reservation, err := os.OpenFile(filepath.Join(root, "RUN-STARTED.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("study already started; do not replay")
	}
	_, err = reservation.Write(fairJSON(map[string]any{"at": time.Now(), "manifest_sha256": fairHash(raw)}))
	_ = reservation.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var finished []string
	for _, plan := range manifest.Schedule {
		t.Logf("starting frozen Session %s", plan.ID)
		if err := runFairSession(ctx, t, root, plan, manifest, cfg, entry); err != nil {
			writeLiveJSON(t, filepath.Join(root, "STOPPED.json"), map[string]any{"at": time.Now(), "session": plan.ID, "finished_sessions": finished, "error_code": errorcode.CodeOf(err), "error_type": fmt.Sprintf("%T", err), "reason": "original error retained in Session/generation/HTTP evidence; stopped without retry or model switch"})
			t.Fatal("frozen study stopped; original evidence retained")
		}
		finished = append(finished, plan.ID)
	}
	writeLiveJSON(t, filepath.Join(root, "COMPLETED.json"), map[string]any{"at": time.Now(), "sessions": finished, "business_assertions": "see per-Turn assertions, including failures"})
}

func runFairSession(ctx context.Context, t *testing.T, study string, plan fairSession, manifest fairManifest, cfg modelconfig.Config, entry string) error {
	root := filepath.Join(study, plan.ID)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	spec, err := fairContext7Spec(os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), entry, workspace)
	if err != nil {
		return err
	}
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{spec}, nil)
	if err != nil {
		return err
	}
	defer manager.Close()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		return ctx.Err()
	}
	definitions := map[string]tool.Definition{}
	for _, one := range manager.Tools() {
		definitions[one.Definition().Name] = one.Definition()
	}
	writeLiveJSON(t, filepath.Join(root, "directory.json"), definitions)
	if len(definitions) < 2 {
		return fmt.Errorf("Context7 directory unavailable")
	}
	sandbox, err := host.New(host.Config{CWD: workspace})
	if err != nil {
		return err
	}
	defer sandbox.Close()
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandbox})
	if err != nil {
		return err
	}
	kept := tools[:0]
	for _, one := range tools {
		if script, ok := one.(*codemode.Tool); ok {
			if plan.Route == "native" {
				continue
			}
			script.Executor = &gojaexec.Executor{Observe: func(metrics gojaexec.Metrics) {
				f, e := os.OpenFile(filepath.Join(root, "worker-metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if e == nil {
					_, _ = f.Write(append(fairJSON(metrics), '\n'))
					_ = f.Close()
				}
			}}
		}
		kept = append(kept, one)
	}
	tools = append(kept, toolsearch.NewSource(manager))
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "code-mode-fair", Workspace: session.WorkspaceRef{Key: plan.ID, CWD: workspace}})
	if err != nil {
		return err
	}
	writeLiveJSON(t, filepath.Join(root, "session-ref.json"), active.SessionRef)
	defer func() {
		if loaded, e := store.LoadSession(context.WithoutCancel(ctx), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true}); e == nil {
			writeLiveJSON(t, filepath.Join(root, "session.json"), loaded)
		}
		if tasks, e := sessionfile.NewTaskStore(store).ListSession(context.WithoutCancel(ctx), active.SessionRef); e == nil {
			writeLiveJSON(t, filepath.Join(root, "tasks.json"), tasks)
		}
	}()
	grants, err := runtime.NewMCPGrantStore(filepath.Join(root, "mcp-grants.json"))
	if err != nil {
		return err
	}
	registry, err := policy.NewMemory(policy.NamedMode{ID: "fair-local", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		deny := func(reason string) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionDeny, Reason: reason}, nil
		}
		if req := in.Tool.ExecutionRequirements; req != nil && req.Sandbox.CommandExec {
			return deny("frozen task prohibits shell/process")
		}
		if tool.IsMCPDefinition(in.Tool) {
			remote, _ := in.Tool.Metadata[tool.MetadataMCPTool].(string)
			if remote != "resolve-library-id" && remote != "query-docs" && remote != "get-library-docs" {
				return deny("only read-only Context7 docs are in scope")
			}
		}
		if in.Tool.Name == codemode.ToolName {
			var input map[string]any
			_ = json.Unmarshal(in.Call.Input, &input)
			if input["file"] != nil {
				return deny("scheme A permits inline JS only")
			}
		}
		var paths map[string]any
		_ = json.Unmarshal(in.Call.Input, &paths)
		for _, key := range []string{"path", "file"} {
			if path, ok := paths[key].(string); ok && path != "" {
				if !filepath.IsAbs(path) {
					path = filepath.Join(workspace, path)
				}
				relative, e := filepath.Rel(workspace, path)
				if e != nil {
					return policy.Decision{}, e
				}
				if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return deny("outside isolated workspace")
				}
				if in.Tool.Name == "Write" || in.Tool.Name == "Patch" {
					if relative != "answer.json" {
						return deny("only answer.json may be written")
					}
				}
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	defer transport.CloseIdleConnections()
	cfg.HTTPClient = &http.Client{Transport: &fairTransport{base: transport, root: root}}
	resolved, err := modelconfig.BuildModel(cfg, 0, 0)
	if err != nil {
		return err
	}
	trace := &fairTrace{LLM: resolved.Model, root: root, turnCalls: map[int]int{}}
	rt, err := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{SystemPrompt: manifest.System}, PolicyRegistry: registry, DefaultPolicyMode: "fair-local", MCPGrants: grants})
	if err != nil {
		return err
	}
	approver := liveApprovalFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		remote, _ := r.Tool.Metadata[tool.MetadataMCPTool].(string)
		if remote == "resolve-library-id" || remote == "query-docs" || remote == "get-library-docs" {
			for _, option := range r.Approval.Options {
				if option.ID == "allow_session" {
					return agent.ApprovalResponse{Outcome: "selected", OptionID: option.ID, Approved: true}, nil
				}
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
	})
	observer := agent.SourceEventObserverFunc(func(_ context.Context, e agent.SourceEvent) error {
		if e.Canonical == nil {
			return nil
		}
		f, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(append(fairJSON(e.Canonical), '\n'))
		return errors.Join(err, f.Close())
	})
	stream := true
	for i, turn := range manifest.Turns {
		turnNo := i + 1
		for name, value := range turn.Inputs {
			if err = os.WriteFile(filepath.Join(workspace, name), []byte(value), 0600); err != nil {
				return err
			}
		}
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.inputs.json", turnNo)), turn.Inputs)
		before, _ := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		var seq uint64
		for _, event := range before {
			seq = max(seq, event.Seq)
		}
		runCtx := context.WithValue(ctx, fairContextKey{}, fairRequestContext{Turn: turnNo})
		run, runErr := rt.Run(runCtx, agent.RunRequest{SessionRef: active.SessionRef, Input: manifest.Prompts[plan.Route][i], Request: agent.ModelRequestOptions{Stream: &stream}, ApprovalRequester: approver, SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: trace, Tools: tools, DeferredTools: manager, Metadata: map[string]any{"reasoning_effort": resolved.ReasoningEffort, codemode.MetadataSelectedTools: gatewayapp.CodeModeFixtureSelectedTools(manager.Tools())}}})
		if runErr == nil {
			runErr = run.Handle.WaitCompletion(runCtx)
		}
		events, e := store.Events(context.WithoutCancel(ctx), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if e != nil {
			return e
		}
		var current []*session.Event
		receipts := map[string]*session.ToolExecution{}
		for _, event := range events {
			if event.Seq > seq {
				current = append(current, event)
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					r := event.Journal.ToolExecution
					receipts[r.Identity] = r
				}
			}
		}
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.events.json", turnNo)), current)
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.receipts.json", turnNo)), receipts)
		answer, answerErr := os.ReadFile(filepath.Join(workspace, "answer.json"))
		if answerErr == nil {
			if e = os.WriteFile(filepath.Join(root, fmt.Sprintf("turn-%d.answer.json", turnNo)), answer, 0600); e != nil {
				return e
			}
		}
		assertions := fairAssertions(answer, answerErr, turn.Total, receipts, definitions)
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.assertions.json", turnNo)), assertions)
		t.Logf("%s Turn %d ended; assertions=%s", plan.ID, turnNo, string(fairJSON(assertions)))
		if runErr != nil {
			return runErr
		}
		for _, r := range receipts {
			if r.Status == session.ToolExecutionUnknownOutcome {
				return fmt.Errorf("unknown original outcome; no replay")
			}
		}
	}
	return nil
}

func fairAssertions(raw []byte, readErr error, total int, receipts map[string]*session.ToolExecution, defs map[string]tool.Definition) map[string]any {
	var answer struct {
		Total  int    `json:"total"`
		Status string `json:"api_status"`
		Answer string `json:"api_answer"`
		Source string `json:"api_source"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&answer)
	reads, queries := 0, 0
	inputNames := map[string]bool{}
	var actual []map[string]any
	for _, r := range receipts {
		if r.Status != session.ToolExecutionSucceeded {
			continue
		}
		if r.ToolName == "Read" {
			var input map[string]any
			_ = json.Unmarshal(r.Input, &input)
			if p, ok := input["path"].(string); ok && (filepath.Base(p) == "a.txt" || filepath.Base(p) == "b.txt") {
				reads++
				inputNames[filepath.Base(p)] = true
			}
		}
		def := defs[r.ToolName]
		remote, _ := def.Metadata[tool.MetadataMCPTool].(string)
		if remote == "query-docs" || remote == "get-library-docs" {
			var input map[string]any
			_ = json.Unmarshal(r.Input, &input)
			actual = append(actual, map[string]any{"tool": r.ToolName, "identity": r.Identity, "input": input, "result_bytes": len(r.Result), "result_sha256": fairHash(r.Result)})
			if input["query"] == fairQuestion {
				queries++
			}
		}
	}
	schema := readErr == nil && err == nil && answer.Answer != "" && len([]rune(answer.Answer)) <= 300 && (answer.Status == "found" || answer.Status == "unavailable")
	return map[string]any{"schema": schema, "expected_total": total, "actual_total": answer.Total, "aggregate": schema && answer.Total == total, "actual_input_read_calls": reads, "both_inputs_read": len(inputNames) == 2, "exact_question_successful_query_calls": queries, "context7_query": queries > 0, "api_evidence_claim": schema && answer.Status == "found" && answer.Source != "", "api_relevance_review": "requires reading actual Context7 result; unavailable/irrelevant is not a passed API assertion", "actual_queries": actual, "answer_bytes": len(raw)}
}

type fairTestModel struct{}

func (fairTestModel) Name() string { return "offline-fixture" }
func (fairTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{TurnComplete: true, Usage: model.Usage{Reported: true, PromptTokens: 1}}), nil)
	}
}

func TestCodeModeFairTraceOffline(t *testing.T) {
	root := t.TempDir()
	trace := &fairTrace{LLM: fairTestModel{}, root: root, turnCalls: map[int]int{}}
	for turn := 1; turn <= 2; turn++ {
		ctx := context.WithValue(t.Context(), fairContextKey{}, fairRequestContext{Turn: turn})
		for _, err := range trace.Generate(ctx, &model.Request{Stream: true, Instructions: []model.Part{model.NewTextPart("fixed")}}) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, suffix := range []string{"request.json", "prefix.json", "stream.jsonl", "first.json", "response.json", "meta.json"} {
		for i := 1; i <= 2; i++ {
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("generation-%03d.%s", i, suffix))); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A mutated instruction cannot silently become another sample.
	for _, err := range trace.Generate(t.Context(), &model.Request{Stream: true, Instructions: []model.Part{model.NewTextPart("changed")}}) {
		if err != nil {
			return
		}
	}
	t.Fatal("changed fixed prefix was accepted")
}
