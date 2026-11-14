package gojaexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// CPUBudget bounds child user/system CPU; idle Host waits are not charged.
const CPUBudget = time.Second

// Metrics describe one child process, including its final wait/reap. CPU covers
// all child user+system time (VM, IPC, GC, startup), not idle Host wall time. RSS
// is OS-reported lifetime high water; it is not a heap measurement or limit.
type Metrics struct {
	PID              int           `json:"pid"`
	Startup          time.Duration `json:"startup_ns"`
	Wall             time.Duration `json:"wall_ns"`
	StopReap         time.Duration `json:"stop_reap_ns"`
	HostDrain        time.Duration `json:"host_drain_ns"`
	CPU              time.Duration `json:"cpu_ns"`
	PeakRSS          int64         `json:"peak_rss_bytes"`
	HostCalls        int           `json:"host_calls"`
	HostRequestBytes int64         `json:"host_request_bytes"`
	HostReplyBytes   int64         `json:"host_reply_bytes"`
	Killed           bool          `json:"killed"`
	Reaped           bool          `json:"reaped"`
	HardMemoryLimit  bool          `json:"hard_memory_limit"`
	ExitCode         int           `json:"exit_code"`
	RPCSentBytes     int64         `json:"rpc_sent_bytes"`
	RPCReceivedBytes int64         `json:"rpc_received_bytes"`
}

// Executor reexecutes its own executable. That executable must route
// WorkerArgument to WorkerMain before application initialization. Observe runs
// after child reaping and Host drain; callers synchronize concurrent observers.
type Executor struct {
	Observe func(Metrics)
}

type workerConfig struct {
	Limits     codemode.Limits `json:"limits"`
	SourceName string          `json:"source_name"`
}
type callRequest struct {
	Method    string          `json:"method,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func effective(l codemode.Limits) (codemode.Limits, error) {
	t := codemode.Tool{Limits: l}
	l, err := t.EffectiveLimits()
	if err != nil {
		return l, err
	}
	// This worker supports tightening the shared defaults, not unbounded transport
	// ceilings selected by a caller or a compromised worker.
	d := codemode.DefaultLimits()
	if l.SourceBytes > d.SourceBytes || l.ArgumentBytes > d.ArgumentBytes || l.ReplyBytes > d.ReplyBytes || l.OutputBytes > d.OutputBytes || l.Calls > d.Calls || l.RetainedBytes > d.RetainedBytes {
		return l, fmt.Errorf("goja worker limits cannot exceed common defaults")
	}
	return l, nil
}

// Execute runs exactly Source.Code after checking its fixed hash. It never
// reopens Source.Path, retries an RPC, or retains variables between invocations.
func (e *Executor) Execute(ctx context.Context, source codemode.Source, limits codemode.Limits, host codemode.Host) (output codemode.Output, resultErr error) {
	if !cpuSupported() {
		return codemode.Output{}, fmt.Errorf("goja worker CPU accounting is implemented only on Darwin/Linux/Windows")
	}
	l, err := effective(limits)
	if err != nil {
		return codemode.Output{}, err
	}
	sum := sha256.Sum256([]byte(source.Code))
	if len(source.Code) == 0 || len(source.Code) > l.SourceBytes || !utf8.ValidString(source.Code) || source.SHA256 != hex.EncodeToString(sum[:]) {
		return codemode.Output{}, fmt.Errorf("invalid fixed source bytes/hash")
	}
	if err := ctx.Err(); err != nil {
		return codemode.Output{}, err
	}
	// Name routes are a fixed snapshot, not discovery schemas or authorization.
	var names []codemode.ToolRoute
	if named, ok := host.(codemode.NamedHost); ok {
		names, err = named.ToolNames(ctx)
		if err != nil {
			return codemode.Output{}, fmt.Errorf("tool names: %w", err)
		}
	}
	remaining := l.ReplyBytes - 2
	for _, name := range names {
		remaining -= 64 // JSON field names, punctuation and element separator.
		for _, value := range []string{name.Name, name.Server, name.Tool, name.Plugin} {
			if len(value) > remaining/6 {
				return codemode.Output{}, fmt.Errorf("tool names exceed reply byte budget")
			}
			remaining -= 6 * len(value)
		}
	}
	namesJSON, err := json.Marshal(names)
	if err != nil || len(namesJSON) > l.ReplyBytes {
		return codemode.Output{}, fmt.Errorf("tool names exceed reply byte budget")
	}
	if err := ctx.Err(); err != nil {
		return codemode.Output{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return codemode.Output{}, err
	}
	dir, err := os.MkdirTemp("", "caelis-goja-worker-")
	if err != nil {
		return codemode.Output{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(executable, WorkerArgument)
	cmd.Dir = dir
	cmd.Env = []string{"GOMAXPROCS=1", "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	input, err := cmd.StdinPipe()
	if err != nil {
		return codemode.Output{}, err
	}
	defer input.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codemode.Output{}, err
	}
	defer stdout.Close()
	// Stderr is discarded, never inherited; VM exceptions cross bounded RPC.
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return codemode.Output{}, err
	}
	m := Metrics{PID: cmd.Process.Pid}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &wireWriter{w: input}
	config, _ := json.Marshal(workerConfig{Limits: l, SourceName: source.Path})
	if err = w.send(frameConfig, 0, config, 32768); err == nil {
		err = w.send(frameNames, 0, namesJSON, l.ReplyBytes)
	}
	if err == nil {
		err = w.send(frameSource, 0, []byte(source.Code), l.SourceBytes)
	}
	type incoming struct {
		f   frame
		err error
	}
	frames := make(chan incoming, 1)
	readerDone := make(chan struct{})
	readerStop := make(chan struct{})
	var received atomic.Int64
	go func() {
		defer close(readerDone)
		for {
			f, err := readFrame(stdout, max(l.ArgumentBytes+2048, l.OutputBytes, 1024))
			if err == nil {
				received.Add(int64(len(f.data) + 13))
			}
			select {
			case frames <- incoming{f, err}:
			case <-readerStop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var hostWG sync.WaitGroup
	var controlWG sync.WaitGroup
	var metricMu sync.Mutex
	var stopAt time.Time
	normal := false
	stop := func() {
		if stopAt.IsZero() {
			stopAt = time.Now()
		}
		cancel()
	}
	defer func() {
		stop()
		// Kill is a backstop for uninterruptible native engine functions. Wait
		// always reaps this original child; no replacement/retry is started.
		if normal {
			select {
			case <-readerDone:
			case <-time.After(100 * time.Millisecond):
				m.Killed = true
				_ = cmd.Process.Kill()
			}
		} else {
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		m.StopReap = time.Since(stopAt)
		_ = input.Close()
		_ = stdout.Close()
		close(readerStop)
		<-readerDone
		hostWaitStart := time.Now()
		hostWG.Wait()
		controlWG.Wait()
		m.HostDrain = time.Since(hostWaitStart)
		m.RPCSentBytes = w.bytes.Load()
		m.RPCReceivedBytes = received.Load()
		m.Wall = time.Since(started)
		m.Reaped = cmd.ProcessState != nil
		if cmd.ProcessState != nil {
			m.ExitCode = cmd.ProcessState.ExitCode()
			m.CPU = cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
			m.PeakRSS = peakRSS(cmd.ProcessState)
		}
		if resultErr == nil && waitErr != nil {
			resultErr = fmt.Errorf("worker exit: %w", waitErr)
		}
		if e.Observe != nil {
			e.Observe(m)
		}
	}()
	if err != nil {
		return codemode.Output{}, err
	}
	// Cancellation uses an out-of-band VM interrupt first, then kill/reap after
	// 100 ms. A blocked Host cannot prevent the process stop (but retains Runtime
	// ownership until the original Host actually exits).
	var stopTimer *time.Timer
	var stopC <-chan time.Time
	ctxC := ctx.Done()
	requests := 0
	for {
		select {
		case <-ctxC:
			resultErr = ctx.Err()
			ctxC = nil
			stop()
			controlWG.Go(func() { _ = w.send(frameCancel, 0, nil, 0) })
			stopTimer = time.NewTimer(100 * time.Millisecond)
			defer stopTimer.Stop()
			stopC = stopTimer.C
		case <-stopC:
			m.Killed = true
			return codemode.Output{}, resultErr
		case next := <-frames:
			if next.err != nil {
				if resultErr != nil {
					return codemode.Output{}, resultErr
				}
				return codemode.Output{}, fmt.Errorf("worker transport: %w", next.err)
			}
			f := next.f
			switch f.kind {
			case frameReady:
				m.Startup = time.Since(started)
			case frameBudget:
				resultErr = fmt.Errorf("worker exceeded 1s CPU budget")
				stop()
				stopTimer = time.NewTimer(100 * time.Millisecond)
				defer stopTimer.Stop()
				stopC = stopTimer.C
			case frameCall:
				requests++
				if workCtx.Err() != nil || f.id == 0 || requests > l.Calls {
					return codemode.Output{}, fmt.Errorf("worker request admission closed or budget exceeded")
				}
				var req callRequest
				if json.Unmarshal(f.data, &req) != nil || len(req.Name) > 1024 || len(req.Arguments) > l.ArgumentBytes || !json.Valid(req.Arguments) || len(req.Arguments) == 0 || req.Arguments[0] != '{' {
					return codemode.Output{}, fmt.Errorf("invalid or oversized worker call")
				}
				metricMu.Lock()
				m.HostRequestBytes += int64(len(f.data))
				m.HostCalls++
				metricMu.Unlock()
				hostWG.Go(func() {
					var reply codemode.Reply
					var callErr error
					switch req.Method {
					case "", "call":
						reply, callErr = host.Call(workCtx, req.Name, req.Arguments)
					case "search":
						var query codemode.SearchRequest
						if err := json.Unmarshal(req.Arguments, &query); err != nil {
							callErr = tool.NewError(tool.ErrorCodeInvalidInput, "invalid search request")
						} else if err := query.Validate(); err != nil {
							callErr = err
						} else if discovery, ok := host.(codemode.DiscoveryHost); ok {
							reply, callErr = discovery.Search(workCtx, query)
						} else {
							callErr = fmt.Errorf("discovery is unavailable")
						}
					default:
						callErr = fmt.Errorf("invalid bridge method")
					}
					// Runtime already bounds Reply before returning it. Check exact
					// bytes again before crossing the process boundary.
					if !boundedReply(reply, l.ReplyBytes) {
						cancel()
						_ = cmd.Process.Kill()
						return
					}
					raw, encodeErr := json.Marshal(reply)
					if encodeErr != nil || len(raw) > l.ReplyBytes {
						cancel()
						_ = cmd.Process.Kill()
						return
					}
					response := struct {
						Raw   string `json:"raw"`
						Error string `json:"error,omitempty"`
						Code  string `json:"code,omitempty"`
					}{Raw: string(raw)}
					if callErr != nil {
						name := req.Name
						if req.Method == "search" {
							name = "search"
						}
						public := codemode.PublicHostError(name, callErr)
						response.Error, response.Code = public.Message, string(public.Code)
					}
					payload, _ := json.Marshal(response)
					metricMu.Lock()
					m.HostReplyBytes += int64(len(raw))
					metricMu.Unlock()
					if w.send(frameReply, f.id, payload, 6*l.ReplyBytes+256) != nil {
						cancel()
						_ = cmd.Process.Kill()
					}
				})
			case frameResult:
				if stopAt.IsZero() {
					stopAt = time.Now()
				}
				if len(f.data) > l.OutputBytes || !json.Valid(f.data) {
					return codemode.Output{}, fmt.Errorf("invalid worker result")
				}
				if resultErr != nil {
					return codemode.Output{}, resultErr
				}
				if err := json.Unmarshal(f.data, &output); err != nil {
					return codemode.Output{}, fmt.Errorf("invalid worker output: %w", err)
				}
				normal = true
				return output, nil
			case frameError:
				if stopAt.IsZero() {
					stopAt = time.Now()
				}
				normal = true
				if resultErr != nil {
					return codemode.Output{}, resultErr
				}
				return codemode.Output{}, tool.NewError(codemode.ErrorCodeJavaScript, "JavaScript execution failed: "+string(f.data))
			default:
				return codemode.Output{}, fmt.Errorf("unexpected worker frame")
			}
		}
	}
}

// Use a conservative JSON escaping bound before allocating a serialized Reply.
// This is transport validation, not a new tool permission or result authority.
func boundedReply(r codemode.Reply, budget int) bool {
	remaining := budget - 128
	consume := func(n int) bool {
		if n > remaining/6 {
			return false
		}
		remaining -= n * 6
		return remaining >= 0
	}
	if remaining < 0 || !consume(len(r.CallID)) || !consume(len(r.Outcome)) {
		return false
	}
	for _, p := range r.Parts {
		remaining -= 64
		if remaining < 0 {
			return false
		}
		for _, s := range []string{p.Kind, p.Text, p.Handle, p.MimeType} {
			if !consume(len(s)) {
				return false
			}
		}
		if !consume(len(p.JSON)) {
			return false
		}
	}
	return true
}
