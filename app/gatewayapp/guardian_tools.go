package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/bwrap"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/seatbelt"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

// guardianQueries is one exclusively leased resident execution environment.
// Review budgets reset independently of the reusable sandbox and SDK Runtime.
type guardianQueries struct {
	mu                                            sync.Mutex
	model                                         model.LLM
	network                                       sandbox.Network
	root, scratch, work                           string
	runtime                                       sandbox.Runtime
	runner                                        systemManagedAgentRunner
	calls, bytes, attempts, toolErrors, truncated int
	resultBytes                                   int
	maxRequestTokens, inputBudget                 int
	errorKinds                                    map[string]int
	modelStarted                                  time.Time
	modelMS, toolMS, setupMS                      int64
	usage                                         model.Usage
}

func (q *guardianQueries) begin(llm model.LLM) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.model = llm
	q.calls, q.bytes, q.attempts, q.toolErrors, q.truncated = 0, 0, 0, 0, 0
	q.modelMS, q.toolMS, q.setupMS = 0, 0, 0
	q.modelStarted = time.Time{}
	q.errorKinds = nil
	q.usage = model.Usage{}
	q.resultBytes = 16 * 1024
	q.maxRequestTokens, q.inputBudget = 0, 0
}

func (q *guardianQueries) invocation(in model.Invocation) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.usage.Reported = q.usage.Reported || in.Usage.IsReported()
	q.usage.PromptTokens += in.Usage.PromptTokens
	q.usage.CachedInputTokens += in.Usage.CachedInputTokens
	q.usage.CompletionTokens += in.Usage.CompletionTokens
	if !q.modelStarted.IsZero() {
		q.modelMS += guardianElapsed(q.modelStarted)
		q.modelStarted = time.Time{}
	}
}
func (q *guardianQueries) metrics(m *guardianReviewMetrics) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m.ModelCalls, m.ToolCalls, m.ToolErrors = q.attempts, q.calls, q.toolErrors
	m.ToolErrorKinds = maps.Clone(q.errorKinds)
	m.ModelMS, m.ToolMS, m.SetupMS = q.modelMS, q.toolMS, q.setupMS
	m.EvidenceBytes, m.TruncatedResults = q.bytes, q.truncated
	m.MaxRequestTokens, m.InputBudgetTokens = q.maxRequestTokens, q.inputBudget
	m.UsageReported, m.InputTokens, m.CachedInputTokens, m.OutputTokens = q.usage.Reported, q.usage.PromptTokens, q.usage.CachedInputTokens, q.usage.CompletionTokens
}
func (q *guardianQueries) finish() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.work == "" {
		return nil
	}
	// work is created by MkdirTemp beneath the immutable private write root.
	relative, err := filepath.Rel(q.scratch, q.work)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid Guardian scratch directory")
	}
	err = os.RemoveAll(q.work)
	q.work = ""
	return err
}
func (q *guardianQueries) open() error {
	if q.runtime != nil {
		return nil
	}
	root, err := os.MkdirTemp("", "caelis-guardian-")
	if err != nil {
		return err
	}
	created := root
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		os.RemoveAll(created)
		return err
	}
	scratch := root
	if runtime.GOOS == "windows" {
		// Keep ACL receipts outside the command's writable directory. Both are
		// private to this resident lane and removed after the runtime closes.
		scratch = filepath.Join(root, "work")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			os.RemoveAll(root)
			return err
		}
	}
	cfg := sandbox.Config{CWD: scratch, ResourceLimits: &sandbox.ResourceLimits{WritePaths: []string{scratch}, Network: q.network}}
	var rt sandbox.Runtime
	switch runtime.GOOS {
	case "darwin":
		rt, err = seatbelt.New(cfg)
	case "linux":
		rt, err = bwrap.New(cfg)
	case "windows":
		cfg.StateDir = filepath.Join(root, "state")
		cfg.HostAuthorityDir = filepath.Join(root, "authority")
		rt, err = windows.New(cfg)
	default:
		err = fmt.Errorf("guardian evidence sandbox is unavailable on %s", runtime.GOOS)
	}
	if err != nil {
		os.RemoveAll(root)
		return err
	}
	q.root = root
	q.scratch = scratch
	q.runtime = rt
	return nil
}
func (q *guardianQueries) close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.runtime != nil {
		if err := q.runtime.Close(); err != nil {
			return err
		}
	}
	if q.root != "" {
		return os.RemoveAll(q.root)
	}
	return nil
}

func (q *guardianQueries) admit(ctx context.Context, req *model.Request) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.model != nil {
		usage := sdkruntime.EvaluateModelRequestBudget(q.model, req, guardianCompactionConfig(q.model, req.Output)).Usage
		q.resultBytes = max(1024, min(64*1024, (usage.EffectiveInputBudget-usage.TotalTokens)/2))
		q.maxRequestTokens = max(q.maxRequestTokens, usage.TotalTokens)
		q.inputBudget = usage.EffectiveInputBudget
		if usage.EffectiveInputBudget > 0 && usage.TotalTokens > usage.EffectiveInputBudget {
			return &model.ContextOverflowError{Cause: fmt.Errorf("guardian exact active request exceeds input budget")}
		}
	}
	q.attempts++
	q.modelStarted = time.Now()
	return nil
}

func (q *guardianQueries) tools() []tool.Tool {
	return []tool.Tool{guardianQueryTool{q, "Read"}, guardianQueryTool{q, "Grep"}, guardianQueryTool{q, "RunCommand"}}
}

type guardianQueryTool struct {
	owner *guardianQueries
	name  string
}

func (t guardianQueryTool) Definition() tool.Definition {
	if t.name == "RunCommand" {
		return tool.Definition{Name: t.name, Description: "Run a focused evidence query in the resident restricted environment. Only the private temporary directory is writable. No escalation is available. Failed or incomplete evidence does not prevent a decision.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}, "additionalProperties": false}}
	}
	d := (&filesystem.SearchTool{}).Definition()
	if t.name == "Read" {
		d = (&filesystem.ReadTool{}).Definition()
	}
	d.ExecutionRequirements = nil
	return d
}

func (t guardianQueryTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	q := t.owner
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	started := time.Now()
	defer func() { q.toolMS += guardianElapsed(started) }()
	q.calls++
	toolCtx := ctx
	setup := time.Now()
	if err := q.open(); err != nil {
		q.setupMS += guardianElapsed(setup)
		return q.errorResult(t.name, "backend_unavailable", err, nil)
	}
	var err error
	if q.scratch != "" && q.work == "" {
		q.work, err = os.MkdirTemp(q.scratch, "review-")
	}
	q.setupMS += guardianElapsed(setup)
	if err != nil {
		return q.errorResult(t.name, "temporary_storage_unavailable", err, nil)
	}
	var result tool.Result
	switch t.name {
	case "Read":
		var read *filesystem.ReadTool
		read, err = filesystem.NewRead(filesystem.DefaultReadConfig(), q.runtime)
		if err == nil {
			result, err = read.Call(toolCtx, call)
		}
	case "Grep":
		var grep *filesystem.SearchTool
		grep, err = filesystem.NewSearch(q.runtime)
		if err == nil {
			result, err = grep.Call(toolCtx, call)
		}
	case "RunCommand":
		var args struct {
			Command string `json:"command"`
		}
		err = json.Unmarshal(call.Input, &args)
		if err == nil && args.Command == "" {
			err = fmt.Errorf("command is required")
		}
		if err == nil {
			dir := q.work
			if dir == "" {
				dir = q.scratch
			}
			command, runErr := q.runtime.Run(toolCtx, sandbox.CommandRequest{Command: args.Command, Dir: dir, Constraints: sandbox.Constraints{Network: q.network}, Env: map[string]string{"TMPDIR": dir, "TMP": dir, "TEMP": dir, "HOME": dir, "PYTHONDONTWRITEBYTECODE": "1"}})
			payload := map[string]any{"stdout": command.Stdout, "stderr": command.Stderr, "exit_code": command.ExitCode}
			if runErr == nil && command.ExitCode != 0 {
				runErr = fmt.Errorf("command exited with code %d", command.ExitCode)
			}
			if runErr != nil {
				return q.errorResult(t.name, "execution_failed", runErr, payload)
			}
			result, err = toolutil.JSONResult(t.name, payload, nil)
		}
	default:
		err = fmt.Errorf("unsupported Guardian tool %q", t.name)
	}
	if err != nil {
		return q.errorResult(t.name, "evidence_unavailable", err, nil)
	}
	return q.boundResult(result), nil
}

func (q *guardianQueries) errorResult(name, kind string, err error, partial map[string]any) (tool.Result, error) {
	q.toolErrors++
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = "deadline_exceeded"
	case errors.Is(err, os.ErrPermission):
		kind = "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		kind = "not_found"
	}
	if q.errorKinds == nil {
		q.errorKinds = map[string]int{}
	}
	q.errorKinds[kind]++
	if partial == nil {
		partial = map[string]any{}
	}
	partial["error_kind"], partial["error"] = kind, guardianFold(err.Error(), 512)
	partial["guidance"] = "This is an evidence limitation, not a risk verdict. Decide using the remaining facts."
	result, _ := toolutil.JSONResult(name, partial, nil)
	result.IsError = true
	return q.boundResult(result), nil
}

// Queries return bounded observations directly to the model. Omitted content
// has no separate history or recovery store.
func (q *guardianQueries) boundResult(result tool.Result) tool.Result {
	budget := q.resultBytes
	if budget <= 0 {
		budget = 16 * 1024
	}
	budget = min(budget, tool.DefaultTruncationPolicy().ByteBudget())
	bounded, info := tool.TruncateResultWithInfo(result, tool.TruncationPolicy{MaxBytes: budget})
	if info.Truncated {
		q.truncated++
	}
	raw, _ := json.Marshal(bounded.Content)
	q.bytes += len(raw)
	return bounded
}
