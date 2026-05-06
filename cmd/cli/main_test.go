package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestResolveInputFromPrompt(t *testing.T) {
	got, single, err := resolveInput("hello", strings.NewReader(""), true)
	if err != nil {
		t.Fatalf("resolveInput() error = %v", err)
	}
	if !single || got != "hello" {
		t.Fatalf("resolveInput() = %q, %v", got, single)
	}
}

func TestResolveTurnInputForceInteractiveDoesNotConsumePipe(t *testing.T) {
	stdin := strings.NewReader("piped prompt")
	got, single, err := resolveTurnInput("", stdin, false, true)
	if err != nil {
		t.Fatalf("resolveTurnInput() error = %v", err)
	}
	if single || got != "" {
		t.Fatalf("resolveTurnInput() = %q, %v", got, single)
	}

	remaining, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatalf("ReadAll(stdin) error = %v", err)
	}
	if string(remaining) != "piped prompt" {
		t.Fatalf("remaining stdin = %q", remaining)
	}
}

func TestParseOutputFormat(t *testing.T) {
	if got, err := parseOutputFormat("json"); err != nil || got != outputJSON {
		t.Fatalf("parseOutputFormat() = %q, %v", got, err)
	}
	if _, err := parseOutputFormat("xml"); err == nil {
		t.Fatal("parseOutputFormat(xml) error = nil")
	}
}

func TestDefaultStoreDirUsesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	want := filepath.Join(home, ".caelis")
	if got := defaultStoreDir(t.TempDir()); got != want {
		t.Fatalf("defaultStoreDir() = %q, want %q", got, want)
	}
}

func TestPreferredSessionIDDefaultsDifferBetweenInteractiveAndHeadless(t *testing.T) {
	if got := preferredInteractiveSessionID(""); got != "" {
		t.Fatalf("preferredInteractiveSessionID(\"\") = %q, want empty for fresh TUI session", got)
	}
	if got := preferredHeadlessSessionID(""); got != "" {
		t.Fatalf("preferredHeadlessSessionID(\"\") = %q, want empty for fresh headless session", got)
	}
	if got := preferredInteractiveSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredInteractiveSessionID(\"sticky\") = %q, want sticky", got)
	}
	if got := preferredHeadlessSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredHeadlessSessionID(\"sticky\") = %q, want sticky", got)
	}
}

func cliTestStoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := []byte(`{"sandbox":{"requested_type":"host"}}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write CLI test config: %v", err)
	}
	return dir
}

func TestStreamHandleWritesAssistantTextAndDeniesApproval(t *testing.T) {
	handle := newFakeHandle([]appgateway.EventEnvelope{
		{
			Event: appgateway.Event{
				Kind:            appgateway.EventKindApprovalRequested,
				ApprovalPayload: &appgateway.ApprovalPayload{Status: appgateway.ApprovalStatusPending},
			},
		},
		{
			Event: appgateway.Event{
				Kind: appgateway.EventKindAssistantMessage,
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleAssistant,
					Text:  "interactive ok",
					Final: true,
				},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "denied by default") {
		t.Fatalf("stderr = %q", got)
	}
	if len(handle.submits) != 1 || handle.submits[0].Approval == nil || handle.submits[0].Approval.Approved {
		t.Fatalf("submits = %#v", handle.submits)
	}
}

func TestStreamHandleIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	handle := newFakeHandle([]appgateway.EventEnvelope{
		{
			Event: appgateway.Event{
				Kind: appgateway.EventKindApprovalReview,
				ApprovalPayload: &appgateway.ApprovalPayload{
					Status:         appgateway.ApprovalStatusPending,
					ReviewStatus:   appgateway.ApprovalReviewStatusInProgress,
					DecisionSource: "auto-review",
				},
			},
		},
		{
			Event: appgateway.Event{
				Kind: appgateway.EventKindAssistantMessage,
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleAssistant,
					Text:  "interactive ok",
					Final: true,
				},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
	if len(handle.submits) != 0 {
		t.Fatalf("submits = %#v, want no manual decision for auto-review event", handle.submits)
	}
}

func TestRunDoctorJSONDoesNotLeakToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-doctor",
		"-format", "json",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "minimax",
		"-model", "MiniMax-M1",
		"-token", "super-secret-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(-doctor) error = %v", err)
	}
	if strings.Contains(out.String(), "super-secret-token") {
		t.Fatalf("doctor output leaked token: %q", out.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("doctor json decode error = %v", err)
	}
	if got := report["active_provider"]; got != "minimax" {
		t.Fatalf("active_provider = %#v, want minimax", got)
	}
}

func TestRunACPSubcommandConstructsStdioServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(ctx, []string{
		"acp",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "acp-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "ollama",
		"-model", "llama3",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(acp) error = %v; stderr=%q", err, errBuf.String())
	}
}

func TestRunDoctorSubcommandTextOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"doctor",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "deepseek",
		"-api", "deepseek",
		"-model", "deepseek-v4-pro",
		"-token-env", "CAELIS_TEST_DOCTOR_TOKEN",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(doctor) error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "active_provider: deepseek") {
		t.Fatalf("doctor text = %q, want active provider line", text)
	}
	if strings.Contains(text, "super-secret") {
		t.Fatalf("doctor text leaked secret: %q", text)
	}
}

type fakeHandle struct {
	events    chan appgateway.EventEnvelope
	submits   []appgateway.SubmitRequest
	closed    bool
	cancelled bool
}

func newFakeHandle(events []appgateway.EventEnvelope) *fakeHandle {
	ch := make(chan appgateway.EventEnvelope, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string { return "h1" }
func (h *fakeHandle) RunID() string    { return "r1" }
func (h *fakeHandle) TurnID() string   { return "t1" }
func (h *fakeHandle) SessionRef() sdksession.SessionRef {
	return sdksession.SessionRef{SessionID: "s1"}
}
func (h *fakeHandle) CreatedAt() time.Time { return time.Time{} }
func (h *fakeHandle) Events() <-chan appgateway.EventEnvelope {
	return h.events
}
func (h *fakeHandle) EventsAfter(string) ([]appgateway.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *fakeHandle) Submit(_ context.Context, req appgateway.SubmitRequest) error {
	h.submits = append(h.submits, req)
	return nil
}
func (h *fakeHandle) Cancel() bool {
	h.cancelled = true
	return true
}
func (h *fakeHandle) Close() error {
	h.closed = true
	return nil
}
