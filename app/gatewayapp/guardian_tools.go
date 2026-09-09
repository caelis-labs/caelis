package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/bwrap"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/seatbelt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

// guardianQueries owns only one review's temporary execution resources. Opening
// a backend is lazy: a review that needs no evidence never needs a query sandbox.
type guardianQueries struct {
	model                              model.LLM
	mu                                 sync.Mutex
	network                            sandbox.Network
	scratch                            string
	runtime                            sandbox.Runtime
	calls, bytes, attempts, inputBytes int
	failure                            error
}

func (q *guardianQueries) open() error {
	if q.runtime != nil {
		return nil
	}
	scratch, err := os.MkdirTemp("", "caelis-guardian-")
	if err != nil {
		return err
	}
	created := scratch
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		os.RemoveAll(created)
		return err
	}
	cfg := sandbox.Config{CWD: scratch, ResourceLimits: &sandbox.ResourceLimits{WritePaths: []string{scratch}, Network: q.network}}
	var rt sandbox.Runtime
	switch runtime.GOOS {
	case "darwin":
		rt, err = seatbelt.New(cfg)
	case "linux":
		rt, err = bwrap.New(cfg)
	default:
		err = fmt.Errorf("guardian evidence sandbox is unavailable on %s", runtime.GOOS)
	}
	if err != nil {
		os.RemoveAll(scratch)
		return err
	}
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
	if q.scratch != "" {
		return os.RemoveAll(q.scratch)
	}
	return nil
}
func (q *guardianQueries) admit(_ context.Context, req *model.Request) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failure != nil {
		return q.failure
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if q.model != nil {
		budget := sdkruntime.EvaluateModelRequestBudget(q.model, req, guardianCompactionConfig(q.model, req.Output)).Usage
		if budget.EffectiveInputBudget > 0 && budget.TotalTokens > budget.EffectiveInputBudget {
			q.failure = fmt.Errorf("guardian active request exceeds input budget")
			return q.failure
		}
	}
	q.attempts++
	q.inputBytes += len(raw)
	if q.attempts > 12 || q.inputBytes > 2*1024*1024 {
		q.failure = fmt.Errorf("guardian cumulative model budget exhausted")
		return q.failure
	}
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
		return tool.Definition{Name: t.name, Description: "Run a local evidence query or script. Only the private temporary directory is writable; TMPDIR points there. Network policy matches the main Agent. Commands complete synchronously; no escalation is available.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}, "additionalProperties": false}}
	}
	d := (&filesystem.SearchTool{}).Definition()
	if t.name == "Read" {
		d = (&filesystem.ReadTool{}).Definition()
	}
	// The private query runtime enforces execution policy; file semantics and
	// the model-visible definition belong to the shared built-in tool.
	d.ExecutionRequirements = nil
	return d
}

func (t guardianQueryTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	q := t.owner
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failure != nil {
		return tool.Result{}, q.failure
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q.calls++
	if q.calls > 8 {
		q.failure = fmt.Errorf("guardian evidence query budget exhausted")
		return tool.Result{}, q.failure
	}
	if err := q.open(); err != nil {
		q.failure = err
		return tool.Result{}, err
	}
	var result tool.Result
	var err error
	switch t.name {
	case "Read":
		var read *filesystem.ReadTool
		read, err = filesystem.NewRead(filesystem.DefaultReadConfig(), q.runtime)
		if err == nil {
			result, err = read.Call(ctx, call)
		}
	case "Grep":
		query, queryErr := filesystem.NewSearch(q.runtime)
		err = queryErr
		if err == nil {
			result, err = query.Call(ctx, call)
		}
	default:
		var args struct {
			Command string `json:"command"`
		}
		err = json.Unmarshal(call.Input, &args)
		if err == nil && args.Command == "" {
			err = fmt.Errorf("command is required")
		}
		if err == nil {
			var command sandbox.CommandResult
			command, err = q.runtime.Run(ctx, sandbox.CommandRequest{Command: args.Command, Dir: q.scratch, Timeout: 10 * time.Second, Constraints: sandbox.Constraints{Network: q.network}, Env: map[string]string{"TMPDIR": q.scratch, "TMP": q.scratch, "TEMP": q.scratch, "HOME": q.scratch, "PYTHONDONTWRITEBYTECODE": "1"}})
			if sandbox.IsCommandExit(err) {
				err = nil
			}
			if err != nil {
				q.failure = fmt.Errorf("guardian evidence execution failed: %w", err)
				return tool.Result{}, q.failure
			}
			if err == nil {
				result, err = toolutil.JSONResult(t.name, map[string]any{"stdout": command.Stdout, "stderr": command.Stderr, "exit_code": command.ExitCode}, nil)
			}
		}
	}
	// Ordinary query errors are visible tool results. The Agent can correct a
	// mistyped path or decide from other evidence; they are not execution-budget
	// failures. Backend creation and resource limits remain fail-closed above.
	if err != nil {
		return tool.Result{}, err
	}
	raw, encodeErr := json.Marshal(result)
	q.bytes += len(raw)
	if err != nil || encodeErr != nil || q.bytes > 128*1024 {
		cause := errors.Join(err, encodeErr)
		if cause != nil {
			q.failure = fmt.Errorf("guardian evidence query failed: %w", cause)
		} else {
			q.failure = fmt.Errorf("guardian evidence query output budget exhausted")
		}
		return tool.Result{}, q.failure
	}
	return result, nil
}

func guardianHistoryPath(ctx context.Context, service session.Service, ref session.SessionRef) string {
	source, ok := service.(interface {
		HistoryPath(context.Context, session.SessionRef) (string, error)
	})
	if !ok {
		return ""
	}
	path, err := source.HistoryPath(ctx, ref)
	if err != nil {
		return ""
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return ""
	}
	return path
}
