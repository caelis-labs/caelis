package cli

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

	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
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

func TestReaderIsTTYUsesInjectedReader(t *testing.T) {
	if readerIsTTY(strings.NewReader("prompt")) {
		t.Fatal("readerIsTTY(strings.Reader) = true, want false for injected non-file stdin")
	}
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()
	if readerIsTTY(file) {
		t.Fatal("readerIsTTY(temp file) = true, want false for regular file")
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

func TestRunHelpReturnsNil(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"-h"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run(-h) error = %v, want nil", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Usage of caelis:") ||
		!strings.Contains(got, "Permission mode: auto-review|manual") {
		t.Fatalf("stderr = %q, want help usage with permission mode", got)
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
	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Event: kernel.Event{
				Kind:            kernel.EventKindApprovalRequested,
				ApprovalPayload: &kernel.ApprovalPayload{Status: kernel.ApprovalStatusPending},
			},
		},
		{
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
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
	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Event: kernel.Event{
				Kind: kernel.EventKindApprovalReview,
				ApprovalPayload: &kernel.ApprovalPayload{
					Status:         kernel.ApprovalStatusPending,
					ReviewStatus:   kernel.ApprovalReviewStatusInProgress,
					DecisionSource: "auto-review",
				},
			},
		},
		{
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
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
	testenv.SetHome(t, t.TempDir())
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
	testenv.SetHome(t, t.TempDir())
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
	testenv.SetHome(t, t.TempDir())
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

func TestRunSandboxSetupSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup) error = %v; stderr=%q", err, errBuf.String())
	}
	text := out.String()
	for _, want := range []string{
		"sandbox_requested_backend: host",
		"sandbox_resolved_backend: host",
		"sandbox_setup_required: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sandbox setup text = %q, want %q", text, want)
		}
	}
}

func TestRunSandboxSetupSubcommandJSONOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-format", "json",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup json) error = %v; stderr=%q", err, errBuf.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("sandbox setup json decode error = %v", err)
	}
	if got := report["ResolvedBackend"]; got != "host" {
		t.Fatalf("ResolvedBackend = %#v, want host", got)
	}
}

func TestRunSandboxSetupSubcommandAcceptsBackendOverride(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", t.TempDir(),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup -sandbox-backend host) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox setup output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxResetSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "reset",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox reset) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox reset output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxCleanSubcommandIsUnknown(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "clean",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "unknown sandbox subcommand: clean") {
		t.Fatalf("run(sandbox clean) error = %v, want unknown subcommand", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty for unknown sandbox clean", out.String())
	}
}

type fakeHandle struct {
	events    chan kernel.EventEnvelope
	submits   []kernel.SubmitRequest
	closed    bool
	cancelled bool
}

func newFakeHandle(events []kernel.EventEnvelope) *fakeHandle {
	ch := make(chan kernel.EventEnvelope, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string { return "h1" }
func (h *fakeHandle) RunID() string    { return "r1" }
func (h *fakeHandle) TurnID() string   { return "t1" }
func (h *fakeHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "s1"}
}
func (h *fakeHandle) CreatedAt() time.Time { return time.Time{} }
func (h *fakeHandle) Events() <-chan kernel.EventEnvelope {
	return h.events
}
func (h *fakeHandle) EventsAfter(string) ([]kernel.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *fakeHandle) Submit(_ context.Context, req kernel.SubmitRequest) error {
	h.submits = append(h.submits, req)
	return nil
}
func (h *fakeHandle) Cancel() kernel.CancelResult {
	h.cancelled = true
	return kernel.CancelResult{Status: kernel.CancelStatusCancelled}
}
func (h *fakeHandle) Close() error {
	h.closed = true
	return nil
}
