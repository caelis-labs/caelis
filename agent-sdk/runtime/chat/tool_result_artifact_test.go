package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestToolResultArtifactStoreWritesRawContent(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 128, 1024*1024)
	jsonRaw := json.RawMessage(`{"result":"exact","count":2}`)
	jsonPath, ok := store.write(tool.Result{Content: []model.Part{model.NewJSONPart(jsonRaw)}})
	if !ok {
		t.Fatal("write(JSON) = false")
	}
	if filepath.Ext(jsonPath) != ".json" {
		t.Fatalf("JSON artifact path = %q, want .json", jsonPath)
	}
	assertFileContent(t, jsonPath, jsonRaw)
	assertPrivateFileMode(t, jsonPath)
	if info, err := os.Stat(store.dir); err != nil {
		t.Fatalf("Stat(artifact dir) error = %v", err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("artifact dir mode = %o, want 700", got)
	}

	text := "first line\nsecond line"
	textPath, ok := store.write(tool.Result{Content: []model.Part{model.NewTextPart(text)}})
	if !ok {
		t.Fatal("write(text) = false")
	}
	if filepath.Ext(textPath) != ".txt" {
		t.Fatalf("text artifact path = %q, want .txt", textPath)
	}
	assertFileContent(t, textPath, []byte(text))

	if mixedPath, ok := store.write(tool.Result{Content: []model.Part{
		model.NewTextPart("alpha"),
		model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceURL, URI: "https://example.com/a.png"}, "image/png", "a"),
		model.NewJSONPart(json.RawMessage(`{"beta":true}`)),
	}}); ok || mixedPath != "" {
		t.Fatalf("write(mixed) = %q/%v, want unsupported", mixedPath, ok)
	}
}

func TestToolResultArtifactStoreCleansOldestFiles(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 2, 1024*1024)
	first, ok := store.write(textToolResult("first"))
	if !ok {
		t.Fatal("write(first) = false")
	}
	second, ok := store.write(textToolResult("second"))
	if !ok {
		t.Fatal("write(second) = false")
	}
	now := time.Now()
	if err := os.Chtimes(first, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Chtimes(first) error = %v", err)
	}
	if err := os.Chtimes(second, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("Chtimes(second) error = %v", err)
	}
	third, ok := store.write(textToolResult("third"))
	if !ok {
		t.Fatal("write(third) = false")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("oldest artifact stat error = %v, want not exist", err)
	}
	for _, path := range []string{second, third} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained artifact %q stat error = %v", path, err)
		}
	}
}

func TestToolResultArtifactStoreCleansExpiredAndOverCapacityFiles(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 8, 10)
	store.maxAge = time.Hour
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Now()
	store.now = func() time.Time { return now }
	expired := filepath.Join(store.dir, "001122334455.txt")
	recent := filepath.Join(store.dir, "112233445566.txt")
	for path, content := range map[string]string{expired: "old", recent: "12345678"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if err := os.Chtimes(expired, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Chtimes(expired) error = %v", err)
	}
	if err := os.Chtimes(recent, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("Chtimes(recent) error = %v", err)
	}
	created, ok := store.write(textToolResult("1234"))
	if !ok {
		t.Fatal("write(created) = false")
	}
	for _, path := range []string{expired, recent} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("evicted artifact %q stat error = %v, want not exist", path, err)
		}
	}
	assertFileContent(t, created, []byte("1234"))
}

func TestToolResultArtifactStoreSkipsOversizedAndUnwritableResults(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 2, 1024)
	store.fileMax = 4
	if path, ok := store.write(textToolResult("too large")); ok || path != "" {
		t.Fatalf("write(oversized) = %q/%v, want skipped", path, ok)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocked) error = %v", err)
	}
	store = &toolResultArtifactStore{dir: blocked, maxAge: time.Hour, maxFiles: 2, maxBytes: 1024, now: time.Now}
	if path, ok := store.write(textToolResult("content")); ok || path != "" {
		t.Fatalf("write(unwritable) = %q/%v, want skipped", path, ok)
	}
}

func TestToolResultArtifactStoreConcurrentWritesUseUniqueNames(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 64, 1024*1024)
	const count = 24
	paths := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if path, ok := store.write(textToolResult("concurrent")); ok {
				paths <- path
			}
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]struct{}{}
	for path := range paths {
		seen[path] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique paths = %d, want %d", len(seen), count)
	}
}

func TestCanonicalTerminalToolResultWritesJSONArtifactAndMergesHint(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 8, 1024*1024)
	large := strings.Repeat("evidence line\n", tool.DefaultTruncationPolicy().ByteBudget()/8)
	raw := mustJSON(map[string]any{
		"result":      large,
		"system_hint": "Keep the original diagnostic hint.",
	})
	result := tool.Result{Content: []model.Part{model.NewJSONPart(raw)}}
	canonical, truncationMeta := canonicalToolResult(result, store)
	if nestedMap(truncationMeta, "caelis", "runtime", "tool", "truncation")["truncated"] != true {
		t.Fatalf("truncation meta = %#v, want truncated", truncationMeta)
	}
	payload := firstToolResultJSONMap(t, canonical)
	hint, _ := payload["system_hint"].(string)
	if !strings.Contains(hint, "Keep the original diagnostic hint.") || !strings.Contains(hint, toolResultArtifactHintPrefix) {
		t.Fatalf("system_hint = %q, want original and artifact hints", hint)
	}
	path := artifactPathFromHint(t, hint)
	assertFileContent(t, path, raw)
	if _, info := tool.TruncateResultWithInfo(canonical, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("canonical result still exceeds policy: %#v", info)
	}
	call := model.ToolCall{ID: "call-json", Name: "ECHO", Args: `{}`}
	message := toolResultMessageFromCanonical(call, canonical)
	event := toolResultEvent(call, canonical, &message, truncationMeta)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(event)); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
	}
}

func TestCanonicalTerminalToolResultAddsTextAndScalarJSONHints(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		result    tool.Result
		extension string
		assert    func(*testing.T, tool.Result) string
	}{
		{
			name:      "text",
			result:    textToolResult(strings.Repeat("text evidence\n", tool.DefaultTruncationPolicy().ByteBudget()/8)),
			extension: ".txt",
			assert: func(t *testing.T, result tool.Result) string {
				t.Helper()
				for _, part := range result.Content {
					if part.Text != nil && strings.Contains(part.Text.Text, toolResultArtifactHintPrefix) {
						return artifactPathFromHint(t, part.Text.Text)
					}
				}
				t.Fatal("canonical text result has no artifact hint")
				return ""
			},
		},
		{
			name: "scalar_json",
			result: tool.Result{Content: []model.Part{model.NewJSONPart(mustJSONValue(t,
				strings.Repeat("scalar evidence", tool.DefaultTruncationPolicy().ByteBudget()/4),
			))}},
			extension: ".json",
			assert: func(t *testing.T, result tool.Result) string {
				t.Helper()
				payload := firstToolResultJSONMap(t, result)
				if _, ok := payload["result"].(string); !ok {
					t.Fatalf("scalar JSON payload = %#v, want wrapped result", payload)
				}
				hint, _ := payload["system_hint"].(string)
				return artifactPathFromHint(t, hint)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := testToolResultArtifactStore(t, 8, 4*1024*1024)
			canonical, _ := canonicalToolResult(tt.result, store)
			path := tt.assert(t, canonical)
			if filepath.Ext(path) != tt.extension {
				t.Fatalf("artifact path = %q, want %s", path, tt.extension)
			}
			want, _, ok := rawToolResultArtifact(tt.result.Content)
			if !ok {
				t.Fatal("rawToolResultArtifact() = false")
			}
			assertFileContent(t, path, want)
		})
	}
}

func TestCanonicalTerminalToolResultDoesNotWriteWithoutTruncationOrAfterWriteFailure(t *testing.T) {
	t.Parallel()

	store := testToolResultArtifactStore(t, 8, 1024*1024)
	canonical, _ := canonicalToolResult(textToolResult("small"), store)
	if strings.Contains(canonical.Content[0].Text.Text, toolResultArtifactHintPrefix) {
		t.Fatalf("untruncated result = %#v, should not contain artifact hint", canonical)
	}
	if _, err := os.Stat(store.dir); !os.IsNotExist(err) {
		t.Fatalf("artifact dir stat error = %v, want not exist", err)
	}

	store.fileMax = 4
	large := textToolResult(strings.Repeat("large", tool.DefaultTruncationPolicy().ByteBudget()))
	canonical, _ = canonicalToolResult(large, store)
	for _, part := range canonical.Content {
		if part.Text != nil && strings.Contains(part.Text.Text, toolResultArtifactHintPrefix) {
			t.Fatalf("write-failed result contains artifact hint: %#v", canonical)
		}
	}
}

func TestToolResultArtifactPathRoundTripsWithoutPersistingFullResult(t *testing.T) {
	t.Parallel()

	artifactStore := testToolResultArtifactStore(t, 8, 1024*1024)
	middleOnly := "MIDDLE-ONLY-ARTIFACT-EVIDENCE"
	large := strings.Repeat("a", tool.DefaultTruncationPolicy().ByteBudget()) + middleOnly + strings.Repeat("z", tool.DefaultTruncationPolicy().ByteBudget())
	result := tool.Result{Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{"result": large}))}}
	canonical, truncationMeta := canonicalToolResult(result, artifactStore)
	call := model.ToolCall{ID: "call-artifact", Name: "ECHO", Args: `{}`}
	message := toolResultMessageFromCanonical(call, canonical)
	resultEvent := toolResultEvent(call, canonical, &message, truncationMeta)
	payload := firstToolResultJSONMap(t, canonical)
	path := artifactPathFromHint(t, payload["system_hint"].(string))
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(artifact) error = %v", err)
	}

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-artifact-roundtrip" },
	}))
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	events := append(modelToolCallEvents(assistant, &model.Response{Message: assistant}), resultEvent)
	for _, event := range events {
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Session: active, Events: loaded.Events}))
	want := []model.Message{assistant, message}
	if !reflect.DeepEqual(replayed, want) {
		t.Fatalf("replayed messages differ\n got: %#v\nwant: %#v", replayed, want)
	}
	if sessionFilesContain(t, root, []byte(middleOnly)) {
		t.Fatalf("session store contains full-only artifact evidence %q", middleOnly)
	}
}

func testToolResultArtifactStore(t *testing.T, maxFiles int, maxBytes int64) *toolResultArtifactStore {
	t.Helper()
	return &toolResultArtifactStore{
		dir:      filepath.Join(t.TempDir(), "tool-results"),
		maxAge:   24 * time.Hour,
		maxFiles: maxFiles,
		maxBytes: maxBytes,
		fileMax:  maxBytes,
		now:      time.Now,
	}
}

func textToolResult(text string) tool.Result {
	return tool.Result{Content: []model.Part{model.NewTextPart(text)}}
}

func mustJSONValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func firstToolResultJSONMap(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	for _, part := range result.Content {
		if part.JSON == nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(part.JSON.Value, &payload); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", part.JSON.Value, err)
		}
		return payload
	}
	t.Fatal("result has no JSON object")
	return nil
}

func artifactPathFromHint(t *testing.T, hint string) string {
	t.Helper()
	idx := strings.LastIndex(hint, toolResultArtifactHintPrefix)
	if idx < 0 {
		t.Fatalf("hint = %q, want artifact path", hint)
	}
	path := strings.TrimSpace(hint[idx+len(toolResultArtifactHintPrefix):])
	if !filepath.IsAbs(path) {
		t.Fatalf("artifact path = %q, want absolute", path)
	}
	return path
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %q content differs: got %d bytes, want %d", path, len(got), len(want))
	}
}

func assertPrivateFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
}

func sessionFilesContain(t *testing.T, root string, needle []byte) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found = bytes.Contains(raw, needle)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%q) error = %v", root, err)
	}
	return found
}
