package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"os/exec"
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
	"github.com/caelis-labs/caelis/control/modelconfig"
)

type valueTask struct {
	ID       string              `json:"id"`
	Output   string              `json:"output"`
	Selected []string            `json:"selected_names"`
	Turns    []string            `json:"common_turn_prompts"`
	Prompts  map[string][]string `json:"exact_prompts"`
}
type valueSession struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	Route string `json:"route"`
}
type valueManifest struct {
	Version     string            `json:"version"`
	ProductHead string            `json:"product_head"`
	Sources     map[string]string `json:"harness_sources_sha256"`
	Inputs      map[string]string `json:"input_sources_sha256"`
	Model       map[string]any    `json:"model"`
	Context7    map[string]any    `json:"context7"`
	System      string            `json:"system"`
	Guidance    map[string]string `json:"entry_guidance"`
	Tasks       []valueTask       `json:"tasks"`
	Schedule    []valueSession    `json:"schedule"`
	Controls    map[string]any    `json:"controls"`
	Benefit     map[string]any    `json:"pre_registered_conditions"`
}

var valueHarnessFiles = []string{"code_mode_value_test.go", "code_mode_value_fixture_test.go", "code_mode_fair_test.go", "code_mode_live_test.go", "code_mode_fixture_test.go", "subagent_cli_main_test.go"}
var valueInputFiles = []string{"records-turn-1.json", "records-turn-2.json", "oracle-turn-1.json", "oracle-turn-2.json", "RULES.md", "context7-schemas.json", "analyze.py"}

func valueManifestFor(head string, parameters, context7 map[string]any, sources, inputs map[string]string) valueManifest {
	guidance := map[string]string{
		"native": "Use native function calls for filesystem work and existing tool_search/mcp_call for extensions. Known extension name missing schema: tool_search with a name string; valid Session schema may be reused directly. Search only unknown names.",
		"js":     "Use js_exec inline code for extension calls: tools.describe(name) takes a name string; tools.call(name,args) calls a native or valid Session schema directly. Unknown names only: tools.search returns {tools:[{name,description}],count,truncated}, not an array. Reuse valid Session schemas across fresh VMs. Native filesystem function calls remain available and may be called directly; JS filesystem orchestration is optional.",
	}
	base := newFairManifest(nil, nil, "")
	common := "Shell/process execution is forbidden, including RunCommand, aliases and equivalents. Work only in this isolated workspace. Native filesystem tools may be called directly in either route; no saved script/workflow.js obligation. Select only relevant tool output. Do not write any file except the specified report. Inputs are read-only. Do not replay unknown effects.\n"
	small := valueTask{ID: "small", Output: "answer.json", Selected: []string{"context7__resolve_library_id", "context7__query_docs"}}
	for _, turn := range base.Turns {
		small.Turns = append(small.Turns, "Known read-only extension names: context7__resolve_library_id and context7__query_docs.\n"+turn.Common+"\nNative filesystem calls are allowed directly; do not assume filesystem work must pass through JS.")
	}
	rules := `Known read-only extension names: workitems__list_batches and workitems__read_batch. The source is a synthetic inspection fixture served by a real local MCP. Call list_batches and read EVERY returned batch in this Turn; use current data, not previous tool output. Results carry structuredContent.
Across ALL records first choose the latest per issue_id by updated_at (UTC RFC3339). Equal timestamp: choose lexicographically greatest batch_id, then greatest record_id (ASCII case-sensitive). Only THEN keep status=open and severity=high or critical. Group by owner. Sort owners ascending and issues within each owner by issue_id ascending.
Write actionable.json with exactly total_count and owners. Each owner entry has exactly owner,count,issues. Each issue has exactly issue_id,severity,updated_at,batch_id,record_id. Counts are counts of selected issues. Do not include evidence payload. Finish with only total_count and per-owner counts.`
	filter := valueTask{ID: "filter", Output: "actionable.json", Selected: []string{"workitems__list_batches", "workitems__read_batch"}, Turns: []string{common + "Turn 1: three batches contain 36 records.\n" + rules, common + "Continue in this SAME Session. R015/ISS-003 is now closed at 2026-11-06T00:00:00Z; R037/ISS-NEW is appended to batch-c (37 total). Re-read all current batches and replace actionable.json.\n" + rules}}
	tasks := []valueTask{small, filter}
	for i := range tasks {
		tasks[i].Prompts = map[string][]string{}
		for route, entry := range guidance {
			for _, turn := range tasks[i].Turns {
				tasks[i].Prompts[route] = append(tasks[i].Prompts[route], entry+"\n\n"+turn)
			}
		}
	}
	return valueManifest{Version: "h1-value-r8-v1", ProductHead: head, Sources: sources, Inputs: inputs, Model: parameters, Context7: context7, System: fairSystem, Guidance: guidance, Tasks: tasks, Schedule: []valueSession{{"small-native", "small", "native"}, {"small-js", "small", "js"}, {"filter-js", "filter", "js"}, {"filter-native", "filter", "native"}}, Controls: map[string]any{
		"tasks": 2, "sessions": 4, "turns_per_session": 2, "scope": "one native/JS pair PER task; order differs between tasks, NOT per-task counterbalance; do not pool tasks", "stream": true, "per_generation_seconds": 600, "max_generations_per_turn_including_selectors": 24, "http_timeout_seconds": 0, "response_header_timeout_seconds": 0, "automatic_retries": 0, "model_switches": 0, "gui": false, "native_fs_direct": true, "report_write_tool": "Write (Patch denied equally)", "saved_script_obligation": false,
		"known_tool_script_errors": "Both routes may repair a known tool/script error inside the original Run within the same 24-generation budget; count and retain every failed step. Never rerun or replace a Session/sample.", "stop": "Unknown outcome, provider/model or Runtime fatal error, incomplete generation, capture error or permanent quota error stops entire study. Business/cost assertion failures are retained and fixed scheduled Turns continue without replacement.", "context7": "External evidence separate from local fixture; unavailable is not full API capability success.", "cache": "Use provider actual usage; no assumption of cold cache or independent Session cache isolation; bytes are not tokens; prefix token components unavailable unless provider supplies them.",
	}, Benefit: map[string]any{
		"business":          "small: total42/43, both actual reads, required exact Context7 question query and valid four-field file; external API relevance is a separate manual evidence assertion. filter: exact frozen oracle and actual list_batches plus each batch read every Turn. No shell/out-of-scope writes.",
		"per_turn_and_pair": "Apply targets to EACH Turn and each same-task pair total. Each task has one pair only; no stable-rate or cross-task aggregate conclusion.", "small": "main requests <= native; main+selector total <= native; fresh <= native; output <= native (all generated script and repair tokens included). Zero widening.", "filter": "main requests < native; main+selector total < native; fresh < native; output <= native (all generated script and repair tokens included). Zero widening.", "missing_usage": "Unavailable/incomplete, not zero; no pass claim or imputation. Native zero denominator: relative delta N/A, absolute delta still judged.", "report": "Business; main/selector/total round trips; provider prompt/cached/fresh/output; outer and child Tool calls; actual original results and selected model-context bytes; subsequent request usage. SDK/HTTP instructions/tools byte equality is not provider KV proof or token attribution.",
	}}
}
func valueSourceHashes() (map[string]string, map[string]string, error) {
	sources, inputs := map[string]string{}, map[string]string{}
	for _, name := range valueHarnessFiles {
		raw, err := os.ReadFile(name)
		if err != nil {
			return nil, nil, err
		}
		sources[name] = fairHash(raw)
	}
	for _, name := range valueInputFiles {
		raw, err := os.ReadFile(filepath.Join(valueDataDir, name))
		if err != nil {
			return nil, nil, err
		}
		inputs[name] = fairHash(raw)
	}
	return sources, inputs, nil
}
func valueHead() (string, error) {
	raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(raw)), err
}
func TestCodeModeValuePrepare(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_VALUE_PREPARE")
	if root == "" {
		t.Skip("offline freeze preparation requires an explicit private artifact path")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute artifact root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("do not overwrite frozen manifest")
	}
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	old, err := os.ReadFile("testdata/code-mode/fair-a/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var prior fairManifest
	if json.Unmarshal(old, &prior) != nil {
		t.Fatal("historical resource summary invalid")
	}
	entry, _ := prior.Context7["entry"].(string)
	raw, err := os.ReadFile(entry)
	if err != nil || fairHash(raw) != prior.Context7["entry_sha256"] {
		t.Fatal("Context7 entry version drift")
	}
	packageRaw, packageErr := os.ReadFile(filepath.Join(filepath.Dir(entry), "..", "package.json"))
	if packageErr != nil || fairHash(packageRaw) != prior.Context7["package_json_sha256"] {
		t.Fatal("Context7 package version drift")
	}
	sources, inputs, err := valueSourceHashes()
	if err != nil {
		t.Fatal(err)
	}
	head, err := valueHead()
	if err != nil {
		t.Fatal(err)
	}
	if err := valueModelBudget(cfg); err != nil {
		t.Fatal(err)
	}
	manifest := valueManifestFor(head, fairModelParameters(cfg), prior.Context7, sources, inputs)
	writeLiveJSON(t, filepath.Join(root, "manifest.json"), manifest)
	frozen, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.sha256"), []byte(fairHash(frozen)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("prepared OFFLINE; manifest_sha256=%s product_head=%s", fairHash(frozen), head)
}
func TestCodeModeValuePlanOffline(t *testing.T) {
	m := valueManifestFor("fixture-head", nil, nil, nil, nil)
	if len(m.Schedule) != 4 || len(m.Tasks) != 2 {
		t.Fatal("frozen sample size changed")
	}
	for _, task := range m.Tasks {
		if len(task.Turns) != 2 {
			t.Fatal("continuation missing")
		}
		for route, entry := range m.Guidance {
			for i, common := range task.Turns {
				if task.Prompts[route][i] != entry+"\n\n"+common {
					t.Fatal("route changed task obligations")
				}
			}
		}
	}
	if !strings.Contains(m.Tasks[1].Turns[0], "Only THEN keep") || m.Controls["native_fs_direct"] != true {
		t.Fatal("dedup/filter order or fs fairness lost")
	}
}

func valueSelected(task valueTask, definitions map[string]tool.Definition) error {
	for _, name := range task.Selected {
		if _, ok := definitions[name]; !ok {
			return fmt.Errorf("selected canonical name missing: %s", name)
		}
	}
	if task.ID == "small" {
		raw, err := os.ReadFile(filepath.Join(valueDataDir, "context7-schemas.json"))
		if err != nil {
			return err
		}
		var snapshot map[string]struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Schema      map[string]any `json:"input_schema"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		for _, name := range task.Selected {
			want, ok := snapshot[name]
			actual := definitions[name]
			if !ok || want.Description != actual.Description || !bytes.Equal(fairJSON(want.Schema), fairJSON(actual.InputSchema)) {
				return fmt.Errorf("Context7 schema/description changed; stop before sampling: %s", name)
			}
		}
	} else {
		for remote, schema := range valueToolSchemas() {
			actual := definitions["workitems__"+remote]
			if !bytes.Equal(fairJSON(schema), fairJSON(actual.InputSchema)) {
				return fmt.Errorf("fixture schema changed")
			}
		}
	}
	return nil
}
func valuePolicy(workspace string, task valueTask) (*policy.MemoryRegistry, error) {
	return policy.NewMemory(policy.NamedMode{ID: "value-local", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		deny := func(why string) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionDeny, Reason: why}, nil
		}
		if in.Tool.Name == "Patch" {
			return deny("both routes use Write for the single report; patch paths are not separately authorized")
		}
		if requirements := in.Tool.ExecutionRequirements; requirements != nil && requirements.Sandbox.CommandExec {
			return deny("frozen task forbids native process identity")
		}
		if tool.IsMCPDefinition(in.Tool) {
			allowed := false
			for _, name := range task.Selected {
				allowed = allowed || name == in.Tool.Name
			}
			if !allowed {
				return deny("only frozen read-only task extensions")
			}
		}
		var input map[string]any
		_ = json.Unmarshal(in.Call.Input, &input)
		if in.Tool.Name == codemode.ToolName && input["file"] != nil {
			return deny("no saved script obligation; inline only")
		}
		for _, key := range []string{"path", "file", "dir", "directory", "cwd"} {
			if path, ok := input[key].(string); ok && path != "" {
				if !filepath.IsAbs(path) {
					path = filepath.Join(workspace, path)
				}
				relative, err := filepath.Rel(workspace, path)
				if err != nil {
					return policy.Decision{}, err
				}
				if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return deny("outside isolated task workspace")
				}
				if (in.Tool.Name == "Write" || in.Tool.Name == "Patch") && relative != task.Output {
					return deny("only frozen report may be written")
				}
			}
		}
		if pattern, ok := input["pattern"].(string); ok && (filepath.IsAbs(pattern) || strings.Contains(pattern, "..")) {
			return deny("outside workspace pattern")
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
}
func valueFilterAssertions(raw []byte, readErr error, turn int, calls []map[string]any) map[string]any {
	oracleRaw, err := os.ReadFile(filepath.Join(valueDataDir, fmt.Sprintf("oracle-turn-%d.json", turn)))
	if err != nil {
		return map[string]any{"capture_error": true}
	}
	var got, want any
	parse := json.Unmarshal(raw, &got)
	_ = json.Unmarshal(oracleRaw, &want)
	list, reads := 0, map[string]int{}
	for _, call := range calls {
		if call["turn"] == turn {
			if call["tool"] == "list_batches" {
				list++
			}
			if call["tool"] == "read_batch" {
				batch, _ := call["batch_id"].(string)
				reads[batch]++
			}
		}
	}
	complete := list > 0 && reads["batch-a"] > 0 && reads["batch-b"] > 0 && reads["batch-c"] > 0
	return map[string]any{"exact_oracle": readErr == nil && parse == nil && reflect.DeepEqual(got, want), "all_batches_actually_read": complete, "list_calls": list, "batch_read_calls": reads, "answer_bytes": len(raw), "oracle_sha256": fairHash(oracleRaw), "classification": "synthetic data; actual local MCP operations"}
}
func valueWirePrefix(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "wire-*.meta.json"))
	if err != nil {
		return err
	}
	var first string
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		var meta map[string]any
		if json.Unmarshal(raw, &meta) != nil {
			return fmt.Errorf("invalid wire metadata")
		}
		if meta["kind"] != "main" {
			continue
		}
		hash, _ := meta["instructions_tools_value_sha256"].(string)
		if first == "" {
			first = hash
		} else if hash != first {
			return fmt.Errorf("HTTP instructions/tools values changed in Session")
		}
	}
	return nil
}
func runValueSession(ctx context.Context, t *testing.T, study string, plan valueSession, task valueTask, manifest valueManifest, cfg modelconfig.Config) error {
	root := filepath.Join(study, plan.ID)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	var spec mcp.ServerSpec
	var fixture *valueFixture
	closeFixture := func() {}
	var err error
	if task.ID == "small" {
		entry, _ := manifest.Context7["entry"].(string)
		spec, err = fairContext7Spec(os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), entry, workspace)
	} else {
		spec, fixture, closeFixture, err = valueFixtureSpec(ctx, root, workspace)
	}
	if err != nil {
		return err
	}
	defer closeFixture()
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
		def := one.Definition()
		definitions[def.Name] = def
	}
	writeLiveJSON(t, filepath.Join(root, "directory.json"), definitions)
	if err := valueSelected(task, definitions); err != nil {
		return err
	}
	sandbox, err := host.New(host.Config{CWD: workspace})
	if err != nil {
		return err
	}
	defer sandbox.Close()
	built, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandbox})
	if err != nil {
		return err
	}
	var tools []tool.Tool
	for _, one := range built {
		if declaration, ok := one.(*codemode.Tool); ok {
			if plan.Route == "native" {
				continue
			}
			declaration.Executor = &gojaexec.Executor{}
		}
		tools = append(tools, one)
	}
	tools = append(tools, toolsearch.NewSource(manager))
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "code-mode-value", Workspace: session.WorkspaceRef{Key: plan.ID, CWD: workspace}})
	if err != nil {
		return err
	}
	writeLiveJSON(t, filepath.Join(root, "session-ref.json"), active.SessionRef)
	defer func() {
		if loaded, err := store.LoadSession(context.WithoutCancel(ctx), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true}); err == nil {
			writeLiveJSON(t, filepath.Join(root, "session.json"), loaded)
		}
		if tasks, err := sessionfile.NewTaskStore(store).ListSession(context.WithoutCancel(ctx), active.SessionRef); err == nil {
			writeLiveJSON(t, filepath.Join(root, "tasks.json"), tasks)
		}
	}()
	registry, err := valuePolicy(workspace, task)
	if err != nil {
		return err
	}
	grants, err := runtime.NewMCPGrantStore(filepath.Join(root, "mcp-grants.json"))
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
	trace := &valueTraceModel{fairTrace: &fairTrace{LLM: resolved.Model, root: root, turnCalls: map[int]int{}}}
	rt, err := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{SystemPrompt: manifest.System}, PolicyRegistry: registry, DefaultPolicyMode: "value-local", MCPGrants: grants})
	if err != nil {
		return err
	}
	approver := liveApprovalFunc(func(_ context.Context, request agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		for _, name := range task.Selected {
			if request.Tool.Name == name {
				for _, option := range request.Approval.Options {
					if option.ID == "allow_session" {
						return agent.ApprovalResponse{Outcome: "selected", OptionID: option.ID, Approved: true}, nil
					}
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			}
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
	})
	observer := agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
		if event.Canonical == nil {
			return nil
		}
		file, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(append(fairJSON(event.Canonical), '\n'))
		return errors.Join(err, file.Close())
	})
	stream := true
	base := newFairManifest(nil, nil, "")
	for turnNo := 1; turnNo <= 2; turnNo++ {
		if fixture != nil {
			if err := fixture.set(turnNo); err != nil {
				return err
			}
			data, _ := valueLoad(turnNo)
			writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.input.json", turnNo)), data)
		} else {
			for name, value := range base.Turns[turnNo-1].Inputs {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte(value), 0600); err != nil {
					return err
				}
			}
			writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.input.json", turnNo)), base.Turns[turnNo-1].Inputs)
		}
		before, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if err != nil {
			return err
		}
		var seq uint64
		for _, event := range before {
			seq = max(seq, event.Seq)
		}
		runCtx := context.WithValue(ctx, fairContextKey{}, fairRequestContext{Turn: turnNo})
		run, runErr := rt.Run(runCtx, agent.RunRequest{SessionRef: active.SessionRef, Input: task.Prompts[plan.Route][turnNo-1], Request: agent.ModelRequestOptions{Stream: &stream}, ApprovalRequester: approver, SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: trace, Tools: tools, DeferredTools: manager, Metadata: map[string]any{"reasoning_effort": resolved.ReasoningEffort, codemode.MetadataSelectedTools: task.Selected}}})
		if runErr == nil {
			runErr = run.Handle.WaitCompletion(runCtx)
		}
		events, err := store.Events(context.WithoutCancel(ctx), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if err != nil {
			return err
		}
		var current []*session.Event
		receipts := map[string]*session.ToolExecution{}
		failed := 0
		for _, event := range events {
			if event.Seq > seq {
				current = append(current, event)
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					receipt := event.Journal.ToolExecution
					receipts[receipt.Identity] = receipt
				}
				if event.Tool != nil && event.Tool.Status == "failed" {
					failed++
				}
			}
		}
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.events.json", turnNo)), current)
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.receipts.json", turnNo)), receipts)
		answer, answerErr := os.ReadFile(filepath.Join(workspace, task.Output))
		if answerErr == nil {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("turn-%d.answer.json", turnNo)), answer, 0600); err != nil {
				return err
			}
		}
		var assertions map[string]any
		if fixture == nil {
			assertions = fairAssertions(answer, answerErr, base.Turns[turnNo-1].Total, receipts, definitions)
		} else {
			fixture.mu.Lock()
			calls := append([]map[string]any(nil), fixture.calls...)
			captureErr := fixture.captureErr
			fixture.mu.Unlock()
			if captureErr != nil {
				return captureErr
			}
			assertions = valueFilterAssertions(answer, answerErr, turnNo, calls)
		}
		assertions["failed_tool_events_including_repairs"] = failed
		writeLiveJSON(t, filepath.Join(root, fmt.Sprintf("turn-%d.assertions.json", turnNo)), assertions)
		if runErr != nil {
			return runErr
		}
		for _, receipt := range receipts {
			if receipt.Status == session.ToolExecutionUnknownOutcome {
				return fmt.Errorf("unknown original outcome; no replay")
			}
		}
		if err := valueWirePrefix(root); err != nil {
			return err
		}
	}
	return nil
}
func TestCodeModeValueLive(t *testing.T) {
	root := os.Getenv("CAELIS_CODE_MODE_VALUE_RUN")
	if root == "" {
		t.Skip("requires Bot-read-back exact manifest, separate authorization and no-replay root")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fairHash(raw) != os.Getenv("CAELIS_CODE_MODE_VALUE_MANIFEST_SHA256") {
		t.Fatal("explicit manifest hash mismatch")
	}
	var manifest valueManifest
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("invalid frozen manifest")
	}
	head, err := valueHead()
	if err != nil || head != manifest.ProductHead {
		t.Fatal("product head changed after freeze")
	}
	if err := exec.Command("git", "diff", "--quiet", "HEAD").Run(); err != nil {
		t.Fatal("tracked source changed after freeze")
	}
	sources, inputs, err := valueSourceHashes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fairJSON(manifest), fairJSON(valueManifestFor(head, manifest.Model, manifest.Context7, sources, inputs))) {
		t.Fatal("task/harness/input contract changed after freeze")
	}
	cfg, cleanup, err := selectedCodeModeModel(t.Context(), os.Getenv("CAELIS_CODE_MODE_LIVE_CONFIG"), os.Getenv("CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT"), authorizedModelID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := valueModelBudget(cfg); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fairJSON(fairModelParameters(cfg)), fairJSON(manifest.Model)) {
		t.Fatal("model parameters changed after freeze")
	}
	entry, _ := manifest.Context7["entry"].(string)
	entryRaw, err := os.ReadFile(entry)
	if err != nil || fairHash(entryRaw) != manifest.Context7["entry_sha256"] {
		t.Fatal("Context7 entry changed")
	}
	reserve, err := os.OpenFile(filepath.Join(root, "RUN-STARTED.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("study already started; never replay")
	}
	_, err = reserve.Write(fairJSON(map[string]any{"at": time.Now(), "head": head, "manifest_sha256": fairHash(raw)}))
	err = errors.Join(err, reserve.Close())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var finished []string
	for _, plan := range manifest.Schedule {
		var task valueTask
		for _, candidate := range manifest.Tasks {
			if candidate.ID == plan.Task {
				task = candidate
			}
		}
		if task.ID == "" {
			t.Fatal("unknown frozen task")
		}
		if err := runValueSession(ctx, t, root, plan, task, manifest, cfg); err != nil {
			writeLiveJSON(t, filepath.Join(root, "STOPPED.json"), map[string]any{"at": time.Now(), "session": plan.ID, "finished": finished, "code": errorcode.CodeOf(err), "type": fmt.Sprintf("%T", err), "reason": "original evidence retained; no retry/replacement/fallback"})
			t.Fatal("frozen study stopped; original evidence retained")
		}
		finished = append(finished, plan.ID)
	}
	writeLiveJSON(t, filepath.Join(root, "COMPLETED.json"), map[string]any{"sessions": finished, "business": "see original per-Turn assertions, including failures"})
}

func valueModelBudget(cfg modelconfig.Config) error {
	if cfg.Timeout > 0 && cfg.Timeout < 600*time.Second {
		return fmt.Errorf("selected total model timeout is shorter than 600 seconds")
	}
	if cfg.StreamFirstEventTimeout > 0 && cfg.StreamFirstEventTimeout < 600*time.Second {
		return fmt.Errorf("selected first-event cutoff is shorter than generation budget")
	}
	return nil
}

type valueTraceModel struct {
	*fairTrace
	captureMu sync.Mutex
	captures  int
}

func (m *valueTraceModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.captureMu.Lock()
		m.captures++
		id := m.captures
		m.captureMu.Unlock()
		var results []map[string]any
		total := 0
		for _, message := range req.Messages {
			for _, part := range message.Parts {
				if part.ToolResult != nil {
					raw := fairJSON(part.ToolResult.Content)
					total += len(raw)
					results = append(results, map[string]any{"name": part.ToolResult.Name, "tool_use_id": part.ToolResult.ToolUseID, "content_bytes": len(raw), "content_sha256": fairHash(raw)})
				}
			}
		}
		link, _ := ctx.Value(fairContextKey{}).(fairRequestContext)
		snapshot := map[string]any{"turn": link.Turn, "request_sha256": fairHash(fairJSON(req)), "sdk_tool_result_content_bytes": total, "results": results, "serialization": "actual Go SDK content JSON; HTTP may encode differently; bytes are not tokens", "prefix_tokens": "unavailable unless provider supplies matching components"}
		if err := os.WriteFile(filepath.Join(m.root, fmt.Sprintf("context-%03d.json", id)), fairJSON(snapshot), 0600); err != nil {
			yield(nil, err)
			return
		}
		for event, err := range m.fairTrace.Generate(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}
