//go:build linux

package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// TestCodeModeDTWAtomicIsolatedX11 uses a deterministic model, production
// Runtime/chat.Factory, fresh Goja workers and one real DTW atomic MCP process.
// It is deliberately opt-in: the ordinary desktop must remain locked throughout.
// Approval is an automated allow_once fixture callback, not a live human/Guardian.
func TestCodeModeDTWAtomicIsolatedX11(t *testing.T) {
	artifacts := os.Getenv("CAELIS_DTW_ATOMIC_ARTIFACTS")
	if artifacts == "" {
		t.Skip("set CAELIS_DTW_ATOMIC_ARTIFACTS, CAELIS_DTW_ATOMIC_BINARY, CAELIS_DTW_ATOMIC_SHA256 and CAELIS_DTW_LOCKED_SESSION to run isolated X11 integration")
	}
	binary := os.Getenv("CAELIS_DTW_ATOMIC_BINARY")
	lockedSession := os.Getenv("CAELIS_DTW_LOCKED_SESSION")
	expectedHash := strings.ToLower(os.Getenv("CAELIS_DTW_ATOMIC_SHA256"))
	if !filepath.IsAbs(artifacts) || !filepath.IsAbs(binary) || strings.TrimSpace(lockedSession) == "" || len(expectedHash) != 64 {
		t.Fatal("require absolute artifact/binary paths, explicit locked session ID and expected SHA256")
	}
	if err := os.Mkdir(artifacts, 0700); err != nil {
		t.Fatal("artifact directory must be new: ", err)
	}
	save := func(name string, value any) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(artifacts, name), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lock := func(stage string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, "loginctl", "show-session", lockedSession, "-p", "LockedHint", "--value").CombinedOutput()
		save("lock-"+stage+".json", map[string]any{"session": lockedSession, "locked_hint": strings.TrimSpace(string(b)), "error": fmt.Sprint(err)})
		if err != nil || strings.TrimSpace(string(b)) != "yes" {
			t.Fatalf("session %s must remain locked: %s %v", lockedSession, b, err)
		}
	}
	lock("before")
	defer lock("after")
	frozen, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(frozen))
	if hash != expectedHash {
		t.Fatal("frozen DTW binary SHA256 mismatch: ", hash)
	}
	save("provenance.json", map[string]any{"binary": binary, "sha256": hash, "source_label": os.Getenv("CAELIS_DTW_ATOMIC_SOURCE"), "model": "deterministic mcpScriptModel; no live model", "approval": "automated fixture allow_once; no live human or Guardian", "platform": "Linux isolated Xvfb + KWin X11"})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": root, "XDG_SESSION_TYPE": "x11", "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_RUNTIME_DIR": filepath.Join(root, "runtime"), "QT_QPA_PLATFORM": "xcb", "KWIN_COMPOSE": "N", "LANG": "C.UTF-8"}
	if err := os.Mkdir(env["XDG_RUNTIME_DIR"], 0700); err != nil {
		t.Fatal(err)
	}
	environment := func() []string {
		var values []string
		for k, v := range env {
			values = append(values, k+"="+v)
		}
		return values
	}
	// Every launched fixture process owns a new process group; cleanup never
	// selects processes by executable name or touches the user's desktop groups.
	var children []*exec.Cmd
	defer func() {
		var cleanup []map[string]any
		for i := len(children) - 1; i >= 0; i-- {
			cmd := children[i]
			killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr := cmd.Wait()
			_, statErr := os.Stat(fmt.Sprintf("/proc/%d", cmd.Process.Pid))
			gone := os.IsNotExist(statErr)
			members, groupErr := dtwAtomicSettledGroup(cmd.Process.Pid)
			cleanup = append(cleanup, map[string]any{"executable": cmd.Args[0], "pid": cmd.Process.Pid, "kill_error": fmt.Sprint(killErr), "wait_error": fmt.Sprint(waitErr), "reaped": cmd.ProcessState != nil, "process_gone": gone, "remaining_group_members": members, "group_error": fmt.Sprint(groupErr)})
			if groupErr != nil {
				t.Error(groupErr)
			}
			if !gone || cmd.ProcessState == nil {
				t.Errorf("fixture child %d was not reaped and removed", cmd.Process.Pid)
			}
		}
		save("fixture-cleanup.json", cleanup)
	}()
	start := func(name string, args []string, extra []*os.File) *exec.Cmd {
		t.Helper()
		log, err := os.OpenFile(filepath.Join(artifacts, name+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = log.Close() })
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = environment()
		cmd.Dir = root
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.ExtraFiles = extra
		cmd.Stdout = log
		cmd.Stderr = log
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
		return cmd
	}
	readDisplay, writeDisplay, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	xvfb := start("xvfb", []string{"Xvfb", "-displayfd", "3", "-screen", "0", "1024x768x24", "-nolisten", "tcp"}, []*os.File{writeDisplay})
	_ = writeDisplay.Close()
	if err = readDisplay.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	number, err := bufio.NewReader(readDisplay).ReadString('\n')
	_ = readDisplay.Close()
	if err != nil {
		t.Fatal(err)
	}
	displayNumber, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil || displayNumber <= 0 {
		t.Fatal("invalid isolated display")
	}
	env["DISPLAY"] = fmt.Sprintf(":%d", displayNumber)
	if env["DISPLAY"] == os.Getenv("DISPLAY") || env["DISPLAY"] == ":0" {
		t.Fatal("refusing inherited/default display")
	}
	kwin := start("kwin", []string{"dbus-run-session", "--", "kwin_x11"}, nil)
	deadline := time.Now().Add(12 * time.Second)
	for {
		probe := exec.CommandContext(ctx, "xprop", "-root", "_NET_SUPPORTING_WM_CHECK")
		probe.Env = environment()
		b, e := probe.CombinedOutput()
		if e == nil && strings.Contains(string(b), "window id #") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("private KWin did not become ready: %s %v", b, e)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	wish, err := exec.LookPath("wish")
	if err != nil {
		t.Fatal(err)
	}
	wish, err = filepath.EvalSymlinks(wish)
	if err != nil {
		t.Fatal(err)
	}
	fixtureName := "DtwCaelisAtomic" + strconv.Itoa(xvfb.Process.Pid)
	executable := filepath.Join(root, fixtureName)
	wishBytes, err := os.ReadFile(wish)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(executable, wishBytes, 0700); err != nil {
		t.Fatal(err)
	}
	version := strings.TrimPrefix(filepath.Base(wish), "wish")
	for variable, prefix := range map[string]string{"TCL_LIBRARY": "tcl", "TK_LIBRARY": "tk"} {
		library := filepath.Join("/usr/share", prefix+version)
		if _, e := os.Stat(library); e != nil {
			t.Fatal(e)
		}
		env[variable] = library
	}
	statePath := filepath.Join(artifacts, "fixture-state.txt")
	geometryPath := filepath.Join(artifacts, "fixture-geometry.json")
	scriptPath := filepath.Join(root, fixtureName+".tcl")
	fixtureSource := fmt.Sprintf("set state_path {%s}\nset geometry_path {%s}\n", statePath, geometryPath) + dtwAtomicFixtureTcl
	if err = os.WriteFile(scriptPath, []byte(fixtureSource), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := start("fixture", []string{executable, scriptPath}, nil)
	var geometry struct {
		X      int   `json:"x"`
		Y      int   `json:"y"`
		Width  int   `json:"width"`
		Height int   `json:"height"`
		Button []int `json:"button"`
		Entry  []int `json:"entry"`
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		b, e := os.ReadFile(geometryPath)
		if e == nil {
			if e = json.Unmarshal(b, &geometry); e != nil {
				t.Fatal(e)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture geometry not ready: ", e)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if geometry.Width != 460 || geometry.Height != 240 || len(geometry.Button) != 4 || len(geometry.Entry) != 4 {
		t.Fatalf("unexpected fixture geometry: %+v", geometry)
	}
	readState := func(stage string, want string) error {
		b, e := os.ReadFile(statePath)
		if e != nil {
			return e
		}
		save("state-"+stage+".json", map[string]string{"raw": string(b)})
		if string(b) != want {
			return fmt.Errorf("fixture state %s: got %q want %q", stage, b, want)
		}
		return nil
	}
	if err = readState("initial", "0\n\n\n"); err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "caelis-dtw-fixture", Name: "dtw", Transport: mcp.TransportStdio, Command: binary, Args: []string{"mcp", "--atomic"}, Env: env, WorkDir: root, CleanEnvironment: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mcpPID := 0
	defer func() {
		closeErr := manager.Close()
		gone := false
		if mcpPID != 0 {
			_, statErr := os.Stat(fmt.Sprintf("/proc/%d", mcpPID))
			gone = os.IsNotExist(statErr)
			if !gone {
				t.Errorf("owned MCP process %d remains after Manager.Close: %v", mcpPID, statErr)
			}
		}
		save("mcp-cleanup.json", map[string]any{"pid": mcpPID, "close_error": fmt.Sprint(closeErr), "close_succeeded": closeErr == nil, "process_gone": gone, "exit_code": nil})
		if closeErr != nil {
			t.Error(closeErr)
		}
	}()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(manager.Tools()) != 15 {
		t.Fatalf("atomic catalog not ready: %+v", manager.GetServerInfos(""))
	}
	definitions := map[string]tool.Definition{}
	for _, target := range manager.Tools() {
		def := target.Definition()
		if def.Metadata[tool.MetadataMCPTool] == "exec" {
			t.Fatal("DTW exposed nested-script exec instead of atomic tools")
		}
		definitions[def.Name] = def
	}
	save("catalog.json", definitions)
	// A real direct child executable identity proves the Manager's transport
	// process survives all VM replacements. No wrapper intercepts MCP traffic.
	mcpPID, err = dtwAtomicChildPID(binary)
	if err != nil {
		t.Fatal(err)
	}
	save("processes.json", map[string]any{"xvfb": xvfb.Process.Pid, "kwin_group": kwin.Process.Pid, "fixture": fixture.Process.Pid, "fixture_executable": executable, "mcp": mcpPID, "display": env["DISPLAY"]})
	appJSON, _ := json.Marshal(executable)
	queryJSON, _ := json.Marshal(fixtureName)
	geometryJSON, _ := json.Marshal(geometry)
	prefix := `const app=` + string(appJSON) + `;const g=` + string(geometryJSON) + `;function data(r){const j=tools.unpack(r).json;for(const x of j){if(x.view||x.apps)return x;}throw Error("missing structured result");}function checked(r){const i=data(r).view.image;if(!(i.width>0&&i.height>0)||JSON.stringify(i.bounds)!==JSON.stringify([g.x,g.y,g.width,g.height]))throw Error("capture/fixture coordinate mismatch");return r;}function point(w,i){const r=g[w];return {app,x:(r[0]+r[2]/2)*i.width/g.width,y:(r[1]+r[3]/2)*i.height/g.height};}`
	token := "caelis_atomic_731"
	scripts := []string{
		prefix + `const r=await dtw.apps({query:` + string(queryJSON) + `,state:"running"});const a=tools.unpack(r).json.find(x=>x.apps).apps;if(a.length!==1||a[0].app!==app)throw Error("fixture identity mismatch");await dtw.look({app});return checked(await dtw.capture({app}));`,
		prefix + `const previousImage=__PREVIOUS_IMAGE__;await dtw.click(point("button",previousImage));return checked(await dtw.capture({app}));`,
		// The click below consumes the previous VM's capture binding, without
		// another capture or focus call replacing the connection's app context.
		prefix + `const previousImage=__PREVIOUS_IMAGE__;await dtw.click(point("entry",previousImage));await dtw.typeText({app,text:"` + token + `"});await dtw.keypress({app,keys:["ENTER"]});await dtw.look({app});return checked(await dtw.capture({app}));`,
		prefix + `await dtw.look({app});return checked(await dtw.capture({app}));`,
	}
	var mu sync.Mutex
	var metrics []gojaexec.Metrics
	executor := &gojaexec.Executor{Observe: func(m gojaexec.Metrics) { mu.Lock(); defer mu.Unlock(); metrics = append(metrics, m) }}
	var approvals []map[string]any
	requester := approvalRequesterFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if r.Tool.Name != r.Call.Name || r.Tool.Metadata[tool.MetadataMCPServer] != "dtw" || r.Call.Parent == nil || r.Call.Parent.Kind != codemode.ToolKind || r.Call.TargetBinding == nil || r.Call.Execution.SessionID == "" || r.Call.Execution.TurnID == "" || r.Call.Execution.ItemID == "" {
			return agent.ApprovalResponse{}, fmt.Errorf("approval lost real MCP target: %s", r.Tool.Name)
		}
		var args map[string]any
		if e := json.Unmarshal(r.Call.Input, &args); e != nil {
			return agent.ApprovalResponse{}, e
		}
		remote, _ := r.Tool.Metadata[tool.MetadataMCPTool].(string)
		if remote == "apps" {
			if args["query"] != fixtureName || args["state"] != "running" {
				return agent.ApprovalResponse{}, fmt.Errorf("unscoped discovery approval")
			}
		} else if args["app"] != executable {
			return agent.ApprovalResponse{}, fmt.Errorf("approval escaped fixture")
		}
		if r.Tool.Metadata[tool.MetadataMCPSourceTrusted] == true {
			return agent.ApprovalResponse{}, fmt.Errorf("source unexpectedly trusted")
		}
		mu.Lock()
		approvals = append(approvals, map[string]any{"target": r.Tool.Name, "remote": remote, "args": args, "approval": r.Approval, "response": "allow_once"})
		mu.Unlock()
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
		if i > 0 {
			image, bitmap, e := dtwAtomicReturnedCapture(request)
			if e != nil {
				t.Error(e)
				return nil
			}
			bounds, _ := json.Marshal(image["bounds"])
			wantBounds, _ := json.Marshal([]int{geometry.X, geometry.Y, geometry.Width, geometry.Height})
			if !bytes.Equal(bounds, wantBounds) {
				t.Errorf("returned capture bounds %s do not match fixture %s", bounds, wantBounds)
				return nil
			}
			stages := []string{"before", "after-button", "after-entry", "final"}
			if e = os.WriteFile(filepath.Join(artifacts, "capture-"+stages[i-1]+".png"), bitmap, 0600); e != nil {
				t.Error(e)
				return nil
			}
			save("capture-"+stages[i-1]+".json", image)
			if i == 1 || i == 2 {
				dimensions, _ := json.Marshal(image)
				scripts[i] = strings.Replace(scripts[i], "__PREVIOUS_IMAGE__", string(dimensions), 1)
			}
		}
		if pid, e := dtwAtomicChildPID(binary); e != nil || pid != mcpPID {
			t.Errorf("MCP process changed between Goja executions: pid=%d error=%v", pid, e)
			return nil
		}
		if i == 2 {
			if e := readState("button", "1\n\n\n"); e != nil {
				t.Error(e)
				return nil
			}
		}
		if i == 3 {
			if e := readState("typed", "1\n"+token+"\n"+token+"\n"); e != nil {
				t.Error(e)
				return nil
			}
		}
		if i >= len(scripts) {
			return nil
		}
		if strings.Contains(scripts[i], "tools.search") || strings.Contains(scripts[i], "__PREVIOUS_IMAGE__") {
			t.Error("invalid transmitted script")
			return nil
		}
		if e := os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("script-%d.js", i+1)), []byte(scripts[i]), 0600); e != nil {
			t.Error(e)
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"code": scripts[i]})
		return []model.ToolCall{{ID: fmt.Sprintf("dtw-script-%d", i+1), Name: codemode.ToolName, Args: string(raw)}}
	}}
	store, active, _ := codeModeStore(t)
	defer func() {
		mu.Lock()
		save("workers.json", metrics)
		save("approvals.json", approvals)
		mu.Unlock()
		evidenceCtx, evidenceCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer evidenceCancel()
		if evidence, e := store.Events(evidenceCtx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true}); e == nil {
			save("canonical-events.json", evidence)
			// Preserve every completed capture even when a later script or state
			// assertion fails before the model-visible snapshot checks complete.
			captureIndex := 0
			for _, event := range evidence {
				if event.Journal == nil || event.Journal.ToolExecution == nil {
					continue
				}
				record := event.Journal.ToolExecution
				if record.ToolName != "dtw__capture" || record.Status != session.ToolExecutionSucceeded {
					continue
				}
				for _, bitmap := range dtwAtomicPNGs(t, record.Result) {
					captureIndex++
					if e := os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("capture-journal-%02d.png", captureIndex)), bitmap, 0600); e != nil {
						t.Error(e)
					}
				}
			}
		} else {
			t.Error(e)
		}
		save("model-requests.json", driver.requests)
	}()
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: executor}}, DeferredTools: manager}, nil, requester)
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	save("canonical-events.json", events)
	save("model-requests.json", driver.requests)
	var journalMedia [][]byte
	var remoteCalls []map[string]any
	defer func() { save("remote-calls.json", remoteCalls) }()
	succeeded := map[string]int{}
	for _, e := range events {
		if e.Journal == nil || e.Journal.ToolExecution == nil {
			continue
		}
		r := e.Journal.ToolExecution
		if r.Parent == nil {
			continue
		}
		switch r.Status {
		case session.ToolExecutionSucceeded:
			succeeded[r.ToolName]++
			def, ok := definitions[r.ToolName]
			if !ok || r.Binding == nil || *r.Binding != tool.BindingForDefinition(def) {
				t.Fatal("nested dispatch lost its admitted target binding")
			}
			remoteCalls = append(remoteCalls, map[string]any{"canonical_target": r.ToolName, "remote": def.Metadata[tool.MetadataMCPTool], "arguments": r.Input, "receipt": r.Result, "binding": r.Binding, "parent": r.Parent})
			journalMedia = append(journalMedia, dtwAtomicPNGs(t, r.Result)...)
		case session.ToolExecutionFailed, session.ToolExecutionUnknownOutcome:
			t.Fatalf("nested call failed: %s %s", r.ToolName, r.Error)
		}
	}
	for remote, want := range map[string]int{"click": 2, "typeText": 1, "keypress": 1, "apps": 1, "capture": 4, "look": 3} {
		canonical := ""
		for name, def := range definitions {
			if def.Metadata[tool.MetadataMCPTool] == remote {
				canonical = name
			}
		}
		if canonical == "" || succeeded[canonical] != want {
			t.Fatalf("remote %s canonical %s executions=%d want=%d", remote, canonical, succeeded[canonical], want)
		}
	}
	if len(journalMedia) < 4 {
		t.Fatalf("canonical journal has only %d PNGs", len(journalMedia))
	}
	for i, b := range journalMedia {
		image, e := png.DecodeConfig(bytes.NewReader(b))
		if e != nil || image.Width <= 0 || image.Height <= 0 {
			t.Fatalf("PNG does not match capture coordinate canvas: %+v %v", image, e)
		}
		if err = os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("capture-%02d.png", i+1)), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Equal(journalMedia[0], journalMedia[len(journalMedia)-1]) {
		t.Fatal("captured image did not change")
	}
	requestsJSON, _ := json.Marshal(driver.requests)
	if len(dtwAtomicPNGs(t, requestsJSON)) == 0 {
		t.Fatal("capture media missing from model context")
	}
	if len(driver.requests) != len(scripts)+1 || len(metrics) != len(scripts) {
		t.Fatalf("model requests=%d workers=%d", len(driver.requests), len(metrics))
	}
	pids := map[int]bool{}
	for _, m := range metrics {
		if !m.Reaped || m.ExitCode != 0 || pids[m.PID] {
			t.Fatalf("worker lifecycle: %+v", m)
		}
		pids[m.PID] = true
	}
	approvalCounts := map[string]int{}
	for _, request := range approvals {
		approvalCounts[request["target"].(string)]++
	}
	for target, count := range succeeded {
		if approvalCounts[target] != count {
			t.Fatalf("target %s succeeded %d times with %d allow_once approvals", target, count, approvalCounts[target])
		}
	}
	if err = readState("final", "1\n"+token+"\n"+token+"\n"); err != nil {
		t.Fatal(err)
	}
	t.Logf("real isolated DTW Runtime/Goja integration evidence: %s (deterministic model, automated fixture approval)", artifacts)
}

// dtwAtomicReturnedCapture reads the preceding script's model-visible result.
// Pixel dimensions come from the actual returned PNG and must agree with DTW's
// image metadata; logical window bounds are a separate coordinate space.
func dtwAtomicReturnedCapture(request *model.Request) (map[string]any, []byte, error) {
	for m := len(request.Messages) - 1; m >= 0; m-- {
		for _, part := range request.Messages[m].Parts {
			if part.ToolResult == nil {
				continue
			}
			var bitmap []byte
			var image map[string]any
			var visit func(any)
			visit = func(value any) {
				switch x := value.(type) {
				case map[string]any:
					if view, ok := x["view"].(map[string]any); ok {
						if candidate, ok := view["image"].(map[string]any); ok {
							image = candidate
						}
					}
					for _, child := range x {
						visit(child)
					}
				case []any:
					for _, child := range x {
						visit(child)
					}
				}
			}
			for _, content := range part.ToolResult.Content {
				if content.Media != nil && content.Media.MimeType == "image/png" {
					if bitmap != nil {
						return nil, nil, fmt.Errorf("preceding script returned multiple PNGs")
					}
					var err error
					bitmap, err = base64.StdEncoding.DecodeString(content.Media.Source.Data)
					if err != nil {
						return nil, nil, err
					}
				}
			}
			raw, err := json.Marshal(part.ToolResult.Content)
			if err != nil {
				return nil, nil, err
			}
			var value any
			if err = json.Unmarshal(raw, &value); err != nil {
				return nil, nil, err
			}
			visit(value)
			if image == nil || bitmap == nil {
				return nil, nil, fmt.Errorf("preceding script did not return capture metadata and PNG")
			}
			pixels, err := png.DecodeConfig(bytes.NewReader(bitmap))
			if err != nil {
				return nil, nil, err
			}
			if pixels.Width <= 0 || pixels.Height <= 0 || image["width"] != float64(pixels.Width) || image["height"] != float64(pixels.Height) {
				return nil, nil, fmt.Errorf("returned PNG dimensions disagree with capture metadata: %dx%d vs %v", pixels.Width, pixels.Height, image)
			}
			return image, bitmap, nil
		}
	}
	return nil, nil, fmt.Errorf("no preceding script tool result")
}

// A killed orphan may remain a zombie until the container's init reaps it.
// Inspect only process-group metadata; no unrelated application state is read.
func dtwAtomicSettledGroup(group int) ([]map[string]string, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		paths, err := filepath.Glob("/proc/[0-9]*/stat")
		if err != nil {
			return nil, err
		}
		var members []map[string]string
		live := false
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			end := bytes.LastIndexByte(data, ')')
			if end < 0 {
				return nil, fmt.Errorf("invalid process group metadata")
			}
			fields := strings.Fields(string(data[end+1:]))
			if len(fields) < 3 {
				return nil, fmt.Errorf("incomplete process group metadata")
			}
			if fields[2] == strconv.Itoa(group) {
				members = append(members, map[string]string{"pid": filepath.Base(filepath.Dir(path)), "state": fields[0]})
				live = live || fields[0] != "Z" && fields[0] != "X"
			}
		}
		if !live {
			return members, nil
		}
		if time.Now().After(deadline) {
			return members, fmt.Errorf("owned fixture group %d still has live processes", group)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dtwAtomicChildPID(binary string) (int, error) {
	var found []int
	// Go's MCP startup may run on another OS thread, so inspect each task's
	// children rather than assuming the current goroutine created the transport.
	tasks, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		return 0, err
	}
	for _, path := range tasks {
		b, e := os.ReadFile(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return 0, e
		}
		for _, s := range strings.Fields(string(b)) {
			pid, e := strconv.Atoi(s)
			if e != nil {
				return 0, e
			}
			exe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			if e == nil && exe == binary {
				found = append(found, pid)
			}
		}
	}
	if len(found) != 1 {
		return 0, fmt.Errorf("expected one directly owned DTW process, got %v", found)
	}
	return found[0], nil
}

// Decode only typed inline media sources; plain text containing base64 is not
// evidence that Runtime delivered an image to the canonical/model context.
func dtwAtomicPNGs(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var images [][]byte
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if mime, _ := x["mime_type"].(string); mime == "image/png" {
				if source, ok := x["source"].(map[string]any); ok {
					if data, ok := source["data"].(string); ok && data != "" {
						b, e := base64.StdEncoding.DecodeString(data)
						if e != nil {
							t.Fatal(e)
						}
						if _, e = png.DecodeConfig(bytes.NewReader(b)); e != nil {
							t.Fatal(e)
						}
						images = append(images, b)
					}
				}
			}
			for _, child := range x {
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	visit(value)
	return images
}

const dtwAtomicFixtureTcl = `wm title . "Caelis owned DTW fixture"
wm geometry . 460x240+50+50
set counter 0
set ack ""
proc save_state {} {
 global state_path counter ack
 set f [open "$state_path.tmp" w]
 puts $f $counter
 puts $f [.entry get]
 puts $f $ack
 close $f
 file rename -force "$state_path.tmp" $state_path
}
label .label -text "Owned Caelis fixture: isolated Xvfb only"
entry .entry -width 40
button .button -text "Increment once" -command {
 incr counter
 .count configure -text "Count=$counter"
 save_state
}
label .count -text "Count=0"
label .ack -text "ACK pending"
pack .label .entry .button .count .ack -padx 12 -pady 6
bind .entry <Return> {
 set ack [.entry get]
 .ack configure -text "ACK=$ack"
 save_state
}
after 500 {
 save_state
 set f [open "$geometry_path.tmp" w]
 puts $f [format {{"x":%d,"y":%d,"width":%d,"height":%d,"button":[%d,%d,%d,%d],"entry":[%d,%d,%d,%d]}} [winfo rootx .] [winfo rooty .] [winfo width .] [winfo height .] [winfo x .button] [winfo y .button] [winfo width .button] [winfo height .button] [winfo x .entry] [winfo y .entry] [winfo width .entry] [winfo height .entry]]
 close $f
 file rename -force "$geometry_path.tmp" $geometry_path
}
`
