package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/internal/kernel/hooks"
	"github.com/caelis-labs/caelis/ports/plugin"
)

func pluginHookEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, ev := range events {
		if ev != nil && ev.Meta["source"] == "plugin_hook" {
			out = append(out, ev)
		}
	}
	return out
}

func TestSessionStartHookRunsOnceAndPersists(t *testing.T) {
	t.Parallel()

	// 1. Create a memory session service correctly
	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	// 2. Define a SessionStart hook.
	hookSpec := plugin.HookSpec{
		PluginID:   "my-test-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": "hello from hook",
		},
	}

	// 3. Create recording runtime and resolver
	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	// 4. Construct gateway with hooks
	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	// 5. Run first turn
	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}

	for range res.Handle.ACPEvents() {
	}

	// Verify that the plugin context event is appended to session history.
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	var pluginContextEvents []*session.Event
	var systemEvents []*session.Event
	for _, ev := range events {
		if ev.Type == session.EventTypeSystem {
			systemEvents = append(systemEvents, ev)
		}
		if ev.Meta["source"] == "plugin_hook" {
			pluginContextEvents = append(pluginContextEvents, ev)
		}
	}

	if len(systemEvents) != 0 {
		t.Fatalf("expected no system events from plugin hook, got %d", len(systemEvents))
	}
	if len(pluginContextEvents) != 1 {
		t.Fatalf("expected 1 plugin context event, got %d", len(pluginContextEvents))
	}

	ev := pluginContextEvents[0]
	if ev.Visibility != session.VisibilityCanonical {
		t.Errorf("expected VisibilityCanonical, got %q", ev.Visibility)
	}
	if ev.Type != session.EventTypeContext {
		t.Fatalf("event type = %q, want context", ev.Type)
	}
	msg, ok := session.ModelMessageOf(ev)
	if !ok {
		t.Fatal("expected plugin context to project to a model message")
	}
	if msg.Role != model.RoleUser {
		t.Fatalf("plugin context model role = %q, want user", msg.Role)
	}
	if !strings.Contains(msg.TextContent(), "hello from hook") || !strings.Contains(msg.TextContent(), "[Plugin context: my-test-plugin]") {
		t.Errorf("unexpected hook text: %q", msg.TextContent())
	}

	// Verify that the session state was updated with hook completion key
	state, err := sessions.SnapshotState(ctx, sess.SessionRef)
	if err != nil {
		t.Fatalf("failed to snapshot state: %v", err)
	}

	hookBytes, err := json.Marshal(hookSpec)
	if err != nil {
		t.Fatalf("failed to marshal hookSpec: %v", err)
	}
	hasher := sha256.New()
	hasher.Write(hookBytes)
	digest := hex.EncodeToString(hasher.Sum(nil))
	stateKey := fmt.Sprintf("plugin.hooks.session_start.v1.%s.%s", hookSpec.PluginID, digest)

	if val, ok := state[stateKey].(bool); !ok || !val {
		t.Errorf("expected stateKey %q to be true, got %v", stateKey, state[stateKey])
	}

	// 6. Run second turn on the same session
	// Wait until the active run is released from the gateway active map to avoid conflict
	for {
		gw.mu.Lock()
		active := gw.active[sess.SessionID]
		gw.mu.Unlock()
		if active == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}

	res2, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello again",
	})
	if err != nil {
		t.Fatalf("Second BeginTurn failed: %v", err)
	}
	for range res2.Handle.ACPEvents() {
	}

	// Verify that NO new plugin context event was appended (total should remain 1)
	events, err = sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events second time: %v", err)
	}

	pluginContextEvents = nil
	for _, ev := range events {
		if ev.Meta["source"] == "plugin_hook" {
			pluginContextEvents = append(pluginContextEvents, ev)
		}
	}

	if len(pluginContextEvents) != 1 {
		t.Errorf("expected plugin context event count to remain 1, got %d", len(pluginContextEvents))
	}
}

func TestSessionStartHookFailure(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	// Define a failing hook using helper process
	failingHookSpec := plugin.HookSpec{
		PluginID:   "failing-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper fail",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER": "1",
			"CAELIS_HOOK_MODE":   "fail",
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{failingHookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn should continue when a SessionStart hook fails, got error: %v", err)
	}
	for range res.Handle.ACPEvents() {
	}

	// Verify that a diagnostic EventTypeLifecycle event is appended to session history
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	var lifecycleEvents []*session.Event
	for _, ev := range events {
		if ev.Type == session.EventTypeLifecycle {
			lifecycleEvents = append(lifecycleEvents, ev)
		}
	}

	if len(lifecycleEvents) != 1 {
		t.Fatalf("expected 1 lifecycle event for diagnosis, got %d", len(lifecycleEvents))
	}
	ev := lifecycleEvents[0]
	if ev.Visibility != session.VisibilityCanonical {
		t.Errorf("expected VisibilityCanonical lifecycle, got %q", ev.Visibility)
	}
	if ev.Meta["source"] != "plugin_hook" || ev.Meta["plugin_id"] != "failing-plugin" {
		t.Errorf("expected top-level plugin hook metadata, got %+v", ev.Meta)
	}
	if ev.Lifecycle == nil || ev.Lifecycle.Status != "failed" {
		t.Errorf("unexpected lifecycle details: %+v", ev.Lifecycle)
	}
	if ev.Lifecycle.Meta["plugin_id"] != "failing-plugin" {
		t.Errorf("expected plugin_id failing-plugin in lifecycle metadata, got %v", ev.Lifecycle.Meta["plugin_id"])
	}
	if rt.lastReq.Input != "hello" {
		t.Fatalf("runtime input = %q, want turn to continue with original input", rt.lastReq.Input)
	}

	hookBytes, err := json.Marshal(failingHookSpec)
	if err != nil {
		t.Fatalf("failed to marshal failingHookSpec: %v", err)
	}
	hasher := sha256.New()
	hasher.Write(hookBytes)
	digest := hex.EncodeToString(hasher.Sum(nil))
	stateKey := fmt.Sprintf("plugin.hooks.session_start.v1.%s.%s", failingHookSpec.PluginID, digest)
	state, err := sessions.SnapshotState(ctx, sess.SessionRef)
	if err != nil {
		t.Fatalf("failed to snapshot state: %v", err)
	}
	if val, ok := state[stateKey].(bool); !ok || !val {
		t.Fatalf("expected failed hook stateKey %q to be true, got %v", stateKey, state[stateKey])
	}

	for {
		gw.mu.Lock()
		active := gw.active[sess.SessionID]
		gw.mu.Unlock()
		if active == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	res2, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello again",
	})
	if err != nil {
		t.Fatalf("Second BeginTurn failed: %v", err)
	}
	for range res2.Handle.ACPEvents() {
	}
	events, err = sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events second time: %v", err)
	}
	lifecycleEvents = nil
	for _, ev := range events {
		if ev.Type == session.EventTypeLifecycle {
			lifecycleEvents = append(lifecycleEvents, ev)
		}
	}
	if len(lifecycleEvents) != 1 {
		t.Fatalf("expected failed SessionStart hook to run once, got %d lifecycle events", len(lifecycleEvents))
	}
}

func TestSessionStartHookResumeWithFileStore(t *testing.T) {
	t.Parallel()

	storeDir := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{
		RootDir: storeDir,
	})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	hookSpec := plugin.HookSpec{
		PluginID:   "my-test-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": "hello from hook",
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	// Construct first gateway and execute turn
	gw1, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res1, err := gw1.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("First BeginTurn failed: %v", err)
	}
	for range res1.Handle.ACPEvents() {
	}

	// Recreate the session service (simulating load/resume) and gateway
	sessions2 := sessionfile.NewStore(sessionfile.Config{
		RootDir: storeDir,
	})

	gw2, err := New(Config{
		Sessions:          sessions2,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("Second gateway failed: %v", err)
	}

	// Begin another turn in the same session
	res2, err := gw2.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello again",
	})
	if err != nil {
		t.Fatalf("Second BeginTurn failed: %v", err)
	}
	for range res2.Handle.ACPEvents() {
	}

	// Verify that the plugin context event is only present once in the persisted file store.
	events, err := sessions2.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	pluginEvents := pluginHookEvents(events)
	if len(pluginEvents) != 1 {
		t.Errorf("expected exactly 1 plugin context event after resume/reload, got %d", len(pluginEvents))
	}
	if len(pluginEvents) == 1 {
		msg, ok := session.ModelMessageOf(pluginEvents[0])
		if !ok {
			t.Fatal("reloaded plugin context does not project to a model message")
		}
		if msg.Role != model.RoleUser {
			t.Fatalf("reloaded plugin context role = %q, want user", msg.Role)
		}
		if !strings.Contains(msg.TextContent(), "hello from hook") {
			t.Fatalf("reloaded plugin context text = %q, want hook output", msg.TextContent())
		}
	}
}

func TestHookDigestDifferentiatesArgsAndEnv(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	// Two hooks in the same plugin, same command, but different args/env
	hook1 := plugin.HookSpec{
		PluginID:   "my-test-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo val-a",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": "val-a",
		},
	}
	hook2 := plugin.HookSpec{
		PluginID:   "my-test-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo val-b",
		PluginDir:  hook1.PluginDir,
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": "val-b",
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hook1, hook2},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	for range res.Handle.ACPEvents() {
	}

	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	var outputs []string
	for _, ev := range pluginHookEvents(events) {
		outputs = append(outputs, session.EventText(ev))
	}

	if len(outputs) != 2 {
		t.Fatalf("expected 2 hook outputs, got %d: %v", len(outputs), outputs)
	}
	if !strings.Contains(outputs[0], "val-a") || !strings.Contains(outputs[1], "val-b") {
		t.Errorf("unexpected hook outputs: %v", outputs)
	}
}

func TestHookEnvAndCompatEnv(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	wsDir := t.TempDir()
	pluginDir := t.TempDir()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: wsDir,
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	hookSpec := plugin.HookSpec{
		PluginID:   "env-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper env",
		PluginDir:  pluginDir,
		Env: map[string]string{
			"CAELIS_HOOK_HELPER": "1",
			"CAELIS_HOOK_MODE":   "env",
			"TEST_VAR":           "my-env-val",
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	for range res.Handle.ACPEvents() {
	}

	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	var output string
	for _, ev := range pluginHookEvents(events) {
		output = session.EventText(ev)
	}

	expected := fmt.Sprintf("my-env-val|%s|%s", pluginDir, wsDir)
	if !strings.Contains(output, expected) {
		t.Errorf("expected env output to contain %q, got %q", expected, output)
	}
}

func TestHookTimeout(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	hookSpec := plugin.HookSpec{
		PluginID:   "timeout-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper sleep",
		PluginDir:  t.TempDir(),
		Timeout:    "50ms",
		Env: map[string]string{
			"CAELIS_HOOK_HELPER": "1",
			"CAELIS_HOOK_MODE":   "sleep",
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn should continue when a SessionStart hook times out, got error: %v", err)
	}
	for range res.Handle.ACPEvents() {
	}
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}
	var lifecycle *session.Event
	for _, ev := range events {
		if ev.Type == session.EventTypeLifecycle {
			lifecycle = ev
			break
		}
	}
	if lifecycle == nil || lifecycle.Lifecycle == nil || lifecycle.Lifecycle.Status != "failed" {
		t.Fatalf("expected failed lifecycle event for timeout, got %#v", lifecycle)
	}
	detail := fmt.Sprint(lifecycle.Lifecycle.Meta["error"], " ", lifecycle.Lifecycle.Meta["stderr"])
	if !strings.Contains(detail, "timeout") && !strings.Contains(detail, "signal: killed") && !strings.Contains(detail, "context deadline exceeded") {
		t.Errorf("unexpected timeout diagnostic: %+v", lifecycle.Lifecycle.Meta)
	}
}

func TestSessionStartHookTruncation(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	largeVal := strings.Repeat("A", 40000)

	hookSpec := plugin.HookSpec{
		PluginID:   "my-large-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": largeVal,
		},
	}

	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	for range res.Handle.ACPEvents() {
	}

	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: sess.SessionRef})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	pluginEvents := pluginHookEvents(events)
	if len(pluginEvents) != 1 {
		t.Fatalf("expected 1 plugin context event, got %d", len(pluginEvents))
	}

	ev := pluginEvents[0]
	text := session.EventText(ev)
	if !strings.HasPrefix(text, "[Plugin context: my-large-plugin]\n") {
		t.Fatalf("unexpected plugin context prefix: %q", text[:min(len(text), 64)])
	}
	if len(text) <= hooks.MaxHookOutputBytes {
		t.Errorf("expected formatted text length to exceed raw hook limit %d, got %d", hooks.MaxHookOutputBytes, len(text))
	}

	truncatedVal, ok := ev.Meta["truncated"].(bool)
	if !ok || !truncatedVal {
		t.Errorf("expected metadata truncated flag to be true, got %v", ev.Meta["truncated"])
	}
}

func TestSessionStartHookRunsOnceAndPersistsEmptyStdout(t *testing.T) {
	t.Parallel()

	// 1. Create a memory session service
	sessions := inmemory.NewStore(inmemory.Config{})
	ctx := context.Background()

	sess, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "test-app",
		UserID:  "test-user",
		Workspace: session.WorkspaceRef{
			Key: "ws-key",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("failed to start session: %v", err)
	}

	// 2. Define a SessionStart hook with empty stdout.
	hookSpec := plugin.HookSpec{
		PluginID:   "my-empty-plugin",
		Event:      plugin.HookEventSessionStart,
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestHookHelperProcess$"},
		RawCommand: "helper echo",
		PluginDir:  t.TempDir(),
		Env: map[string]string{
			"CAELIS_HOOK_HELPER":   "1",
			"CAELIS_HOOK_MODE":     "echo",
			"CAELIS_HOOK_ECHO_VAL": "", // empty stdout
		},
	}

	// 3. Create recording runtime and resolver
	rt := &recordingRuntime{result: agent.RunResult{}}
	resolver := staticResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: sess.SessionRef,
			Input:      "hello",
		},
	}}

	// 4. Construct gateway with hooks
	gw, err := New(Config{
		Sessions:          sessions,
		Runtime:           rt,
		Resolver:          resolver,
		SessionStartHooks: []plugin.HookSpec{hookSpec},
	})
	if err != nil {
		t.Fatalf("New gateway failed: %v", err)
	}

	// 5. Run first turn
	res, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}

	// Wait for the background turn to finish
	for range res.Handle.ACPEvents() {
	}

	// Verify that the plugin marker event is appended to session history.
	events, err := sessions.Events(ctx, session.EventsRequest{
		SessionRef:       sess.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("failed to list session events: %v", err)
	}

	pluginEvents := pluginHookEvents(events)
	if len(pluginEvents) != 1 {
		t.Fatalf("expected 1 plugin marker event, got %d", len(pluginEvents))
	}

	ev := pluginEvents[0]
	if ev.Visibility != session.VisibilityMirror {
		t.Errorf("expected VisibilityMirror, got %q", ev.Visibility)
	}
	if ev.Type != session.EventTypeCustom {
		t.Fatalf("event type = %q, want custom", ev.Type)
	}
	if got := session.EventText(ev); got != "" {
		t.Errorf("unexpected hook marker text: %q", got)
	}

	// Calculate the state key
	hookBytes, err := json.Marshal(hookSpec)
	if err != nil {
		t.Fatalf("failed to marshal hookSpec: %v", err)
	}
	hasher := sha256.New()
	hasher.Write(hookBytes)
	digest := hex.EncodeToString(hasher.Sum(nil))
	stateKey := fmt.Sprintf("plugin.hooks.session_start.v1.%s.%s", hookSpec.PluginID, digest)

	// Verify state key is set
	state, err := sessions.SnapshotState(ctx, sess.SessionRef)
	if err != nil {
		t.Fatalf("failed to snapshot state: %v", err)
	}
	if val, ok := state[stateKey].(bool); !ok || !val {
		t.Errorf("expected stateKey %q to be true, got %v", stateKey, state[stateKey])
	}

	// Now delete the state key to force the fallback event scanning logic
	_, err = sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: sess.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(st map[string]any) (map[string]any, error) {
		delete(st, stateKey)
		return st, nil
	}})
	if err != nil {
		t.Fatalf("failed to delete state key: %v", err)
	}

	// Run second turn on the same session
	for {
		gw.mu.Lock()
		active := gw.active[sess.SessionID]
		gw.mu.Unlock()
		if active == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}

	res2, err := gw.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: sess.SessionRef,
		Input:      "hello again",
	})
	if err != nil {
		t.Fatalf("Second BeginTurn failed: %v", err)
	}
	for range res2.Handle.ACPEvents() {
	}

	// Verify that NO new plugin marker event was appended (total should remain 1)
	events, err = sessions.Events(ctx, session.EventsRequest{
		SessionRef:       sess.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("failed to list session events second time: %v", err)
	}

	pluginEvents = pluginHookEvents(events)
	if len(pluginEvents) != 1 {
		t.Errorf("expected plugin marker event count to remain 1 (due to fallback event scanning), got %d", len(pluginEvents))
	}
}

func TestHookHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_HOOK_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CAELIS_HOOK_MODE")
	switch mode {
	case "echo":
		val := os.Getenv("CAELIS_HOOK_ECHO_VAL")
		fmt.Print(val)
		os.Exit(0)
	case "fail":
		os.Exit(1)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "env":
		fmt.Printf("%s|%s|%s", os.Getenv("TEST_VAR"), os.Getenv("CAELIS_PLUGIN_DIR"), os.Getenv("CAELIS_WORKSPACE_DIR"))
		os.Exit(0)
	}
}
