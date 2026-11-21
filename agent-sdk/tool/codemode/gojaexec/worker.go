package gojaexec

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/dop251/goja"
)

//go:embed bootstrap.js
var bootstrap string

// WorkerMain owns one VM for the process lifetime. Pipe readers and the CPU
// watchdog only interrupt it; all JS values and Promise jobs stay on this owner.
func WorkerMain() int {
	w := &wireWriter{w: os.Stdout}
	vm := goja.New()
	vm.SetMaxCallStackSize(1024)
	done := make(chan struct{})
	defer close(done)
	frames := make(chan frame, 1)
	go func() {
		for {
			f, err := readFrame(os.Stdin, 6*codemode.DefaultLimits().ReplyBytes+256, false)
			if err != nil {
				vm.Interrupt("parent transport closed")
				close(frames)
				return
			}
			if f.kind == frameCancel {
				vm.Interrupt("execution canceled")
				continue
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()
	var cpuMu sync.Mutex
	var cpuStart time.Duration
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cpuMu.Lock()
				if cpuStart > 0 && processCPU()-cpuStart >= CPUBudget {
					vm.Interrupt("1s CPU budget exceeded")
					_ = w.send(frameBudget, 0, nil, 0)
					cpuStart = 0
				}
				cpuMu.Unlock()
			case <-done:
				return
			}
		}
	}()
	pending := map[uint64]func(any) error{}
	var next uint64
	var calls int
	var limits codemode.Limits
	rpc := func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := vm.NewPromise()
		next++
		calls++
		if calls > limits.Calls {
			_ = reject("Host request budget exceeded")
			return vm.ToValue(promise)
		}
		kind, name, args := call.Argument(0).String(), call.Argument(1).String(), call.Argument(2).String()
		if (kind != "call" && kind != "search") || len(name) > 1024 {
			_ = reject("invalid Host request")
			return vm.ToValue(promise)
		}
		data, err := json.Marshal(callRequest{Method: kind, Name: name, Arguments: json.RawMessage(args)})
		if err == nil {
			err = w.send(frameCall, next, data, len(data))
		}
		if err != nil {
			vm.Interrupt("Host transport failed")
			_ = reject("Host transport failed")
		} else {
			pending[next] = resolve
		}
		return vm.ToValue(promise)
	}
	factory, err := vm.RunString(bootstrap)
	if err != nil {
		return 2
	}
	build, ok := goja.AssertFunction(factory)
	if !ok {
		return 2
	}
	bridge, err := build(goja.Undefined(), vm.ToValue(rpc))
	if err != nil {
		return 2
	}
	obj := bridge.ToObject(vm)
	decodeJSON, _ := goja.AssertFunction(obj.Get("parse"))
	configure, _ := goja.AssertFunction(obj.Get("configure"))
	prepare, _ := goja.AssertFunction(obj.Get("prepare"))
	encode, _ := goja.AssertFunction(obj.Get("encode"))
	plain := func(value any) (goja.Value, error) {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return decodeJSON(goja.Undefined(), vm.ToValue(string(raw)))
	}
	for {
		f, open := <-frames
		if !open {
			return 0
		}
		if f.kind == frameClose {
			return 0
		}
		if f.kind != frameConfig {
			return 2
		}
		var cfg workerConfig
		if json.Unmarshal(f.data, &cfg) != nil {
			return 2
		}
		limits, err = effective(cfg.Limits)
		if err != nil {
			return 2
		}
		var names strings.Builder
		for {
			f, open = <-frames
			if !open || f.kind != frameNames || len(f.data) > namesFrameBytes {
				return 2
			}
			if len(f.data) == 0 {
				break
			}
			names.Write(f.data)
		}
		f, open = <-frames
		if !open || f.kind != frameSource || len(f.data) > limits.SourceBytes {
			return 2
		}
		sourceName := cfg.SourceName
		if sourceName == "" {
			sourceName = "inline.js"
		}
		cpuMu.Lock()
		vm.ClearInterrupt()
		cpuStart = max(1, processCPU())
		cpuMu.Unlock()
		limitsValue, e1 := plain(limits)
		namesValue, e2 := plain(json.RawMessage(names.String()))
		stateValue, e3 := plain(cfg.State)
		if e1 != nil || e2 != nil || e3 != nil {
			return 2
		}
		if _, err = configure(goja.Undefined(), limitsValue, namesValue, stateValue); err != nil {
			return 2
		}
		calls = 0
		if w.send(frameReady, 0, nil, 0) != nil {
			return 2
		}
		script, runErr := prepareSource(sourceName, string(f.data))
		if runErr == nil {
			declarations, e := plain(script.declarations)
			if e != nil {
				return 2
			}
			var publish goja.Value
			publish, runErr = prepare(goja.Undefined(), declarations)
			if runErr == nil {
				for _, code := range script.hoists {
					if _, runErr = vm.RunScript(sourceName, code); runErr != nil {
						break
					}
				}
			}
			var value goja.Value
			if runErr == nil {
				value, runErr = vm.RunScript(sourceName, script.code)
			}
			if runErr == nil {
				run, ok := goja.AssertFunction(value)
				if !ok {
					return 2
				}
				value, runErr = run(goja.Undefined(), publish)
			}
			if runErr == nil {
				promise, ok := value.Export().(*goja.Promise)
				if !ok {
					return 2
				}
				for promise.State() == goja.PromiseStatePending && runErr == nil {
					if len(pending) == 0 {
						runErr = fmt.Errorf("unsettled Promise without a Host producer")
						break
					}
					select {
					case f, open := <-frames:
						if !open {
							return 2
						}
						resolve, ok := pending[f.id]
						if f.kind != frameReply || !ok {
							return 2
						}
						delete(pending, f.id)
						runErr = resolve(string(f.data))
					case <-time.After(10 * time.Millisecond):
						_, runErr = vm.RunString("void 0")
					}
				}
				if runErr == nil && promise.State() == goja.PromiseStateRejected {
					message := promise.Result().String()
					if object, ok := promise.Result().(*goja.Object); ok {
						if stack := object.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
							for _, line := range strings.Split(stack.String(), "\n") {
								if at := strings.Index(line, sourceName+":"); at >= 0 {
									tail := line[at+len(sourceName)+1:]
									if end := strings.IndexByte(tail, ':'); end > 0 {
										message += "\n" + sourceName + ":" + tail[:end]
									}
									break
								}
							}
						}
					}
					runErr = fmt.Errorf("%s", message)
				}
				if runErr == nil {
					var encoded goja.Value
					encoded, runErr = encode(goja.Undefined(), promise.Result())
					if runErr == nil && len(pending) == 0 {
						cpuMu.Lock()
						cpuStart = 0
						cpuMu.Unlock()
						if w.send(frameResult, 0, []byte(encoded.String()), limits.OutputBytes) != nil {
							return 2
						}
						continue
					}
				}
			}
		}
		cpuMu.Lock()
		cpuStart = 0
		cpuMu.Unlock()
		if len(pending) != 0 {
			_ = w.send(frameFatal, 0, []byte("script returned with unawaited Host requests"), limits.OutputBytes)
			return 1
		}
		if runErr == nil {
			runErr = fmt.Errorf("invalid script completion")
		}
		var interrupted *goja.InterruptedError
		if errors.As(runErr, &interrupted) {
			_ = w.send(frameFatal, 0, []byte("execution interrupted"), limits.OutputBytes)
			return 1
		}
		workerError(w, limits.OutputBytes, runErr)
	}
}

func workerError(w *wireWriter, budget int, err error) {
	message := err.Error()
	// Encoder exceptions are already safe, field-specific diagnostics; their
	// bootstrap stack is not user source.
	var exception *goja.Exception
	if errors.As(err, &exception) {
		message = exception.Value().String()
	}
	if len(message) > budget {
		const suffix = " [diagnostic exceeds output budget]"
		if budget > len(suffix) {
			message = message[:budget-len(suffix)] + suffix
		} else {
			message = message[:budget]
		}
	}
	_ = w.send(frameError, 0, []byte(message), budget)
}
