package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const noOutputPlaceholder = "(no output)"

type completedTaskSession struct {
	entry *taskapi.Entry
}

func (s completedTaskSession) Ref() sandbox.SessionRef {
	if s.entry == nil {
		return sandbox.SessionRef{}
	}
	return sandbox.SessionRef{
		Backend:   s.entry.Terminal.Backend,
		SessionID: s.entry.Terminal.SessionID,
	}
}

func (s completedTaskSession) Terminal() sandbox.TerminalRef {
	if s.entry == nil {
		return sandbox.TerminalRef{}
	}
	return sandbox.CloneTerminalRef(s.entry.Terminal)
}

func (completedTaskSession) WriteInput(_ context.Context, _ []byte) error {
	return fmt.Errorf("agent-sdk/runtime: task is not running")
}

func (s completedTaskSession) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	if s.entry == nil || s.entry.Result == nil {
		return nil, nil, 0, 0, nil
	}
	stdout, stderr := completedTaskOutput(s.entry.Result)
	if stdout == noOutputPlaceholder && stderr == "" {
		stdout = ""
	}
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	if stdoutMarker > int64(len(stdout)) {
		stdoutMarker = int64(len(stdout))
	}
	if stderrMarker > int64(len(stderr)) {
		stderrMarker = int64(len(stderr))
	}
	return []byte(stdout[stdoutMarker:]), []byte(stderr[stderrMarker:]), int64(len(stdout)), int64(len(stderr)), nil
}

func (s completedTaskSession) AwaitOutput(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputObservation, error) {
	stdout, stderr := "", ""
	if s.entry != nil {
		stdout, stderr = completedTaskOutput(s.entry.Result)
		if stdout == noOutputPlaceholder && stderr == "" {
			stdout = ""
		}
	}
	available := sandbox.OutputCursor{
		Stdout: int64(len([]byte(stdout))),
		Stderr: int64(len([]byte(stderr))),
	}
	cursor = sandbox.NormalizeOutputCursor(cursor)
	if err := sandbox.ValidateOutputCursor(cursor, available); err != nil {
		return sandbox.OutputObservation{}, err
	}
	status, err := s.Status(ctx)
	if err != nil {
		return sandbox.OutputObservation{}, err
	}
	return sandbox.OutputObservation{
		Cursor: available,
		Status: status,
	}, nil
}

func (s completedTaskSession) Status(context.Context) (sandbox.SessionStatus, error) {
	if s.entry == nil {
		return sandbox.SessionStatus{}, nil
	}
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       false,
		SupportsInput: false,
		ExitCode:      completedTaskExitCode(s.entry),
		StartedAt:     s.entry.CreatedAt,
		UpdatedAt:     s.entry.UpdatedAt,
	}, nil
}

func (s completedTaskSession) Wait(ctx context.Context, _ time.Duration) (sandbox.SessionStatus, error) {
	return s.Status(ctx)
}

func (s completedTaskSession) Result(context.Context) (sandbox.CommandResult, error) {
	if s.entry == nil || s.entry.Result == nil {
		return sandbox.CommandResult{}, nil
	}
	stdout, stderr := completedTaskOutput(s.entry.Result)
	return sandbox.CommandResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: completedTaskExitCode(s.entry),
		Route:    sandbox.RouteHost,
		Backend:  s.entry.Terminal.Backend,
	}, nil
}

func (completedTaskSession) Terminate(context.Context) error { return nil }

func completedTaskOutput(result map[string]any) (string, string) {
	if result == nil {
		return "", ""
	}
	if text, _ := result["result"].(string); text != "" {
		return text, ""
	}
	return "", ""
}

func completedTaskExitCode(entry *taskapi.Entry) int {
	if entry == nil {
		return 0
	}
	if code, ok := parseIntArgValue(entry.Result["exit_code"]); ok {
		return code
	}
	state := entry.State
	if state == "" {
		state = taskapi.State(strings.TrimSpace(taskStringValue(entry.Result["state"])))
	}
	switch state {
	case taskapi.StateCompleted:
		return 0
	case taskapi.StateCancelled, taskapi.StateInterrupted:
		return -1
	case taskapi.StateFailed:
		return 1
	default:
		if entry.Running {
			return 0
		}
		return 1
	}
}

func sandboxRuntimeFromTool(tool tool.Tool) (sandbox.Runtime, bool) {
	provider, ok := tool.(sandboxRuntimeProvider)
	if !ok || provider == nil {
		return nil, false
	}
	runtime := provider.SandboxRuntime()
	if runtime == nil {
		return nil, false
	}
	return runtime, true
}

type commandTimeoutProvider interface {
	CommandTimeout() time.Duration
}

func commandTimeoutFromTool(tool tool.Tool) time.Duration {
	provider, ok := tool.(commandTimeoutProvider)
	if !ok || provider == nil {
		return 0
	}
	timeout := provider.CommandTimeout()
	if timeout < 0 {
		return 0
	}
	return timeout
}

func constraintsFromMetadata(meta map[string]any) sandbox.Constraints {
	if meta == nil {
		return sandbox.Constraints{}
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sandbox.Constraints{}
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed)
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}
	}
	var out sandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sandbox.Constraints{}
	}
	return sandbox.NormalizeConstraints(out)
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	var out map[string]any
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func stringArg(values map[string]any, key string) (string, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func intArg(values map[string]any, key string) (int, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return 0, false
	}
	return parseIntArgValue(raw)
}

func optionalIntArg(values map[string]any, key string) *int {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	value, ok := parseIntArgValue(raw)
	if !ok {
		return nil
	}
	return &value
}

func optionalBoolArg(values map[string]any, key string) *bool {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	value, ok := parseBoolArgValue(raw)
	if !ok {
		return nil
	}
	return &value
}

func parseIntArgValue(raw any) (int, bool) {
	switch typed := raw.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		value, err := typed.Int64()
		return int(value), err == nil
	case string:
		value, err := strconv.Atoi(strings.TrimSpace(typed))
		return value, err == nil
	default:
		return 0, false
	}
}

func parseBoolArgValue(raw any) (bool, bool) {
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		value, err := strconv.ParseBool(strings.TrimSpace(typed))
		return value, err == nil
	default:
		return false, false
	}
}

func taskInt64Value(raw any) (int64, bool) {
	switch typed := raw.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	default:
		return 0, false
	}
}

func compactLatestOutput(delta string) string {
	delta = strings.ReplaceAll(strings.ReplaceAll(delta, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(delta) == "" {
		return ""
	}
	trailingNewline := strings.HasSuffix(delta, "\n")
	rawLines := strings.Split(strings.TrimRight(delta, "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, compactLine(line))
	}
	if len(lines) == 0 {
		return ""
	}
	const keepLines = 5
	if len(lines) > keepLines {
		hidden := len(lines) - keepLines
		lines = append([]string{fmt.Sprintf("...%d lines hidden...", hidden)}, lines[len(lines)-keepLines:]...)
	}
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out
}

func terminalDeltaText(stdout string, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return joinTerminalStreams(stdout, stderr)
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

func terminalOutputText(output string, stdout string, stderr string) string {
	if text := terminalDeltaText(stdout, stderr); text != "" {
		return text
	}
	return output
}

func terminalFinalText(output string, stdout string, stderr string, resultErr error) string {
	if text := terminalDeltaText(stdout, stderr); taskOutputHasNonBlankLine(text) {
		return text
	}
	if taskOutputHasNonBlankLine(output) {
		return output
	}
	if resultErr != nil {
		if text := strings.TrimSpace(resultErr.Error()); text != "" {
			if !sandbox.IsCommandExit(resultErr) {
				return text
			}
		}
	}
	return noOutputPlaceholder
}

func compactFinalOutput(stdout, stderr string) string {
	switch {
	case taskOutputHasNonBlankLine(stdout) && taskOutputHasNonBlankLine(stderr):
		return compactBlock(joinTerminalStreams(stdout, stderr), 1600)
	case taskOutputHasNonBlankLine(stdout):
		return compactBlock(stdout, 1600)
	case taskOutputHasNonBlankLine(stderr):
		return compactBlock(stderr, 1600)
	default:
		return ""
	}
}

func joinTerminalStreams(stdout string, stderr string) string {
	if stdout == "" || stderr == "" {
		return stdout + stderr
	}
	if strings.HasSuffix(stdout, "\n") || strings.HasSuffix(stdout, "\r") ||
		strings.HasPrefix(stderr, "\n") || strings.HasPrefix(stderr, "\r") {
		return stdout + stderr
	}
	return stdout + "\n" + stderr
}

func compactBlock(text string, limit int) string {
	if !taskOutputHasNonBlankLine(text) || limit <= 0 || len(text) <= limit {
		return text
	}
	const marker = "\n...[truncated]...\n"
	head := limit / 2
	tail := limit - head - len(marker)
	if tail < 0 {
		tail = 0
	}
	return text[:head] + marker + text[len(text)-tail:]
}

func compactLine(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	const limit = 160
	if len(text) <= limit {
		return text
	}
	const marker = " ...[truncated]... "
	head := 70
	tail := limit - head - len(marker)
	if tail < 0 {
		tail = 0
	}
	return text[:head] + marker + text[len(text)-tail:]
}
