package gojaexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
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
// after process reaping; Runtime independently drains its Host producers.
// Callers synchronize concurrent observers.
type Executor struct {
	Observe func(Metrics)
}

type workerConfig struct {
	Limits     codemode.Limits         `json:"limits"`
	SourceName string                  `json:"source_name"`
	State      codemode.ExecutionState `json:"state"`
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

// NewEnvironment creates an independent, lazily started Goja process.
func (e *Executor) NewEnvironment() codemode.Environment {
	return &environment{executor: e, generation: uuid.NewString()}
}

// Execute supplies an isolated environment for standalone SDK callers. Runtime
// uses NewEnvironment and retains it with its Session activation.
func (e *Executor) Execute(ctx context.Context, source codemode.Source, limits codemode.Limits, host codemode.Host) (codemode.Output, error) {
	env := e.NewEnvironment()
	defer env.Close()
	return env.Execute(ctx, source, limits, host)
}

type environment struct {
	mu           sync.Mutex
	executor     *Executor
	generation   string
	child        *workerProcess
	busy, closed bool
}

type workerProcess struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	output    *os.File
	writer    *wireWriter
	done      chan struct{}
	dir       string
	started   time.Time
	mu        sync.Mutex
	metrics   Metrics
	waitErr   error
	closeOnce sync.Once
	executor  *Executor
}

func startWorker(e *Executor) (*workerProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "caelis-goja-worker-")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, WorkerArgument)
	configureWorkerProcess(cmd)
	cmd.Dir = dir
	cmd.Env = []string{"GOMAXPROCS=1", "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	input, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	output, writeEnd, err := os.Pipe()
	if err != nil {
		input.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	cmd.Stdout = writeEnd
	child := &workerProcess{cmd: cmd, input: input, output: output, done: make(chan struct{}), dir: dir, started: time.Now(), executor: e}
	child.writer = &wireWriter{w: input}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		writeEnd.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	writeEnd.Close()
	child.metrics.PID = cmd.Process.Pid
	go func() {
		err := cmd.Wait()
		child.mu.Lock()
		child.waitErr = err
		child.metrics.Reaped = cmd.ProcessState != nil
		if cmd.ProcessState != nil {
			child.metrics.ExitCode = cmd.ProcessState.ExitCode()
			child.metrics.CPU = cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
			child.metrics.PeakRSS = peakRSS(cmd.ProcessState)
		}
		child.mu.Unlock()
		close(child.done)
	}()
	return child, nil
}

func (p *workerProcess) close() error {
	p.closeOnce.Do(func() {
		at := time.Now()
		select {
		case <-p.done:
		default:
			go func() { _ = p.writer.send(frameClose, 0, nil, 0) }()
			select {
			case <-p.done:
			case <-time.After(100 * time.Millisecond):
				p.mu.Lock()
				p.metrics.Killed = true
				p.mu.Unlock()
				_ = p.cmd.Process.Kill()
			}
		}
		_ = p.input.Close()
		// Process termination is independent of callbacks still owned by Runtime.
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
		_ = p.output.Close()
		p.mu.Lock()
		p.metrics.StopReap = time.Since(at)
		p.metrics.Wall = time.Since(p.started)
		p.metrics.RPCSentBytes = p.writer.bytes.Load()
		m := p.metrics
		p.mu.Unlock()
		if p.executor.Observe != nil {
			p.executor.Observe(m)
		}
		_ = os.RemoveAll(p.dir)
	})
	return nil
}

func (e *environment) Close() error {
	e.mu.Lock()
	e.closed = true
	p := e.child
	e.mu.Unlock()
	if p != nil {
		return p.close()
	}
	return nil
}

func (e *environment) Execute(ctx context.Context, source codemode.Source, limits codemode.Limits, host codemode.Host) (output codemode.Output, resultErr error) {
	if !cpuSupported() {
		return output, fmt.Errorf("goja CPU accounting is unavailable")
	}
	l, err := effective(limits)
	if err != nil {
		return output, err
	}
	sum := sha256.Sum256([]byte(source.Code))
	if len(source.Code) == 0 || len(source.Code) > l.SourceBytes || !utf8.ValidString(source.Code) || source.SHA256 != hex.EncodeToString(sum[:]) {
		return output, fmt.Errorf("invalid fixed source bytes/hash")
	}
	if err = ctx.Err(); err != nil {
		return output, err
	}
	var names []codemode.ToolRoute
	if named, ok := host.(codemode.NamedHost); ok {
		names, err = named.ToolNames(ctx)
		if err != nil {
			return output, err
		}
	}
	namesJSON, err := json.Marshal(names)
	if err != nil {
		return output, err
	}
	e.mu.Lock()
	if e.closed || e.busy {
		e.mu.Unlock()
		return output, fmt.Errorf("JavaScript environment is closed or busy; observe its original Task before the next script")
	}
	if e.child == nil {
		e.child, err = startWorker(e.executor)
		if err != nil {
			e.mu.Unlock()
			return output, err
		}
	}
	p := e.child
	e.busy = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.busy = false; e.mu.Unlock() }()
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fatal := true
	defer func() {
		if fatal {
			_ = e.Close()
		}
	}()
	stopCancel := context.AfterFunc(workCtx, func() {
		go func() { _ = p.writer.send(frameCancel, 0, nil, 0) }()
		time.AfterFunc(100*time.Millisecond, func() { _ = p.cmd.Process.Kill(); _ = p.input.Close() })
	})
	defer stopCancel()
	state := codemode.ExecutionState{Generation: e.generation, Execution: uuid.NewString()}
	if one, ok := host.(codemode.StateHost); ok {
		state = one.ExecutionState()
	}
	config, _ := json.Marshal(workerConfig{Limits: l, SourceName: source.Path, State: state})
	if err = p.writer.send(frameConfig, 0, config, 32768); err == nil {
		for len(namesJSON) > 0 && err == nil {
			n := min(len(namesJSON), namesFrameBytes)
			err = p.writer.send(frameNames, 0, namesJSON[:n], namesFrameBytes)
			namesJSON = namesJSON[n:]
		}
	}
	if err == nil {
		err = p.writer.send(frameNames, 0, nil, namesFrameBytes)
	}
	if err == nil {
		err = p.writer.send(frameSource, 0, []byte(source.Code), l.SourceBytes)
	}
	if err != nil {
		return output, err
	}
	type incoming struct {
		f   frame
		err error
	}
	frames := make(chan incoming, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			f, err := readFrame(p.output, max(l.ArgumentBytes+2048, l.OutputBytes, 1024))
			if err == nil {
				p.mu.Lock()
				p.metrics.RPCReceivedBytes += int64(len(f.data) + 13)
				p.mu.Unlock()
			}
			select {
			case frames <- incoming{f, err}:
			case <-workCtx.Done():
				return
			}
			if err != nil || f.kind == frameResult || f.kind == frameError || f.kind == frameFatal {
				return
			}
		}
	}()
	var hosts sync.WaitGroup
	// Late replies are confined to this process and request IDs. They never gain
	// a reference to a replacement environment or its current Host.
	defer func() {
		stopCancel()
		cancel()
		if fatal {
			_ = p.close()
		}
		drained := make(chan struct{})
		go func() { hosts.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(100 * time.Millisecond):
		}
	}()
	requests := 0
	for {
		select {
		case <-workCtx.Done():
			if ctx.Err() != nil {
				return output, ctx.Err()
			}
			return output, fmt.Errorf("worker reply or transport budget exceeded")
		case next := <-frames:
			if next.err != nil {
				return output, fmt.Errorf("worker transport: %w", next.err)
			}
			f := next.f
			switch f.kind {
			case frameReady:
				p.mu.Lock()
				if p.metrics.Startup == 0 {
					p.metrics.Startup = time.Since(p.started)
				}
				p.mu.Unlock()
			case frameBudget:
				return output, fmt.Errorf("worker exceeded 1s CPU budget")
			case frameCall:
				requests++
				if workCtx.Err() != nil || f.id == 0 || requests > l.Calls {
					return output, fmt.Errorf("worker request admission closed or budget exceeded")
				}
				var req callRequest
				if json.Unmarshal(f.data, &req) != nil || len(req.Name) > 1024 || len(req.Arguments) > l.ArgumentBytes || !json.Valid(req.Arguments) || len(req.Arguments) == 0 || req.Arguments[0] != '{' {
					return output, fmt.Errorf("invalid worker call")
				}
				p.mu.Lock()
				p.metrics.HostCalls++
				p.metrics.HostRequestBytes += int64(len(f.data))
				p.mu.Unlock()
				hosts.Go(func() {
					var reply codemode.Reply
					var callErr error
					switch req.Method {
					case "", "call":
						reply, callErr = host.Call(workCtx, req.Name, req.Arguments)
					case "search":
						var query codemode.SearchRequest
						if err := json.Unmarshal(req.Arguments, &query); err != nil {
							callErr = err
						} else if err = query.Validate(); err != nil {
							callErr = err
						} else if d, ok := host.(codemode.DiscoveryHost); ok {
							reply, callErr = d.Search(workCtx, query)
						} else {
							callErr = fmt.Errorf("discovery is unavailable")
						}
					default:
						callErr = fmt.Errorf("invalid bridge method")
					}
					if workCtx.Err() != nil {
						return
					}
					if !boundedReply(reply, l.ReplyBytes) {
						cancel()
						return
					}
					reply.BodyText = reply.Text()
					if !boundedReply(reply, l.ReplyBytes) {
						cancel()
						return
					}
					raw, err := json.Marshal(reply)
					if err != nil || len(raw) > l.ReplyBytes {
						cancel()
						return
					}
					response := struct {
						Raw   string `json:"raw"`
						Error string `json:"error,omitempty"`
						Code  string `json:"code,omitempty"`
					}{Raw: string(raw)}
					if callErr != nil {
						public := codemode.PublicHostError(req.Name, callErr)
						response.Error, response.Code = public.Message, string(public.Code)
					}
					payload, _ := json.Marshal(response)
					p.mu.Lock()
					p.metrics.HostReplyBytes += int64(len(raw))
					p.mu.Unlock()
					if p.writer.send(frameReply, f.id, payload, 6*l.ReplyBytes+256) != nil {
						cancel()
					}
				})
			case frameResult:
				if err := workCtx.Err(); err != nil {
					return output, err
				}
				if len(f.data) > l.OutputBytes || json.Unmarshal(f.data, &output) != nil {
					return output, fmt.Errorf("invalid worker result")
				}
				hosts.Wait()
				<-readerDone
				fatal = false
				return output, nil
			case frameError:
				if err := workCtx.Err(); err != nil {
					return output, err
				}
				<-readerDone
				fatal = false
				return output, tool.NewError(codemode.ErrorCodeJavaScript, "JavaScript execution failed: "+string(f.data))
			case frameFatal:
				<-readerDone
				return output, fmt.Errorf("JavaScript environment stopped: %s", f.data)
			default:
				return output, fmt.Errorf("unexpected worker frame")
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
	if remaining < 0 || !consume(len(r.CallID)) || !consume(len(r.Outcome)) || !consume(len(r.BodyText)) {
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
