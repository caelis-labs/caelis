package gojaexec

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dop251/goja"
)

//go:embed bootstrap.js
var bootstrap string

type settlement struct{ resolve, reject func(any) error }

// WorkerMain runs the dedicated pipe protocol and returns a process exit code.
// The caller must invoke it only for WorkerArgument and immediately os.Exit.
// There is exactly one VM owner goroutine: this one. Readers and the watchdog
// use only Runtime.Interrupt, the engine's documented concurrent exception.
func WorkerMain() int {
	w := &wireWriter{w: os.Stdout}
	f, err := readFrame(os.Stdin, 32768)
	if err != nil || f.kind != frameConfig {
		return 2
	}
	var cfg workerConfig
	if json.Unmarshal(f.data, &cfg) != nil {
		return 2
	}
	l, err := effective(cfg.Limits)
	if err != nil {
		return 2
	}
	f, err = readFrame(os.Stdin, l.ReplyBytes)
	if err != nil || f.kind != frameNames || !json.Valid(f.data) {
		return 2
	}
	namesJSON := string(f.data)
	f, err = readFrame(os.Stdin, l.SourceBytes)
	if err != nil || f.kind != frameSource {
		return 2
	}
	code := string(f.data)
	vm := goja.New()
	vm.SetMaxCallStackSize(1024)
	done := make(chan struct{})
	defer close(done)
	responses := make(chan frame, 1)
	go func() {
		for {
			f, err := readFrame(os.Stdin, 6*l.ReplyBytes+256)
			if err != nil {
				vm.Interrupt("parent transport closed")
				return
			}
			if f.kind == frameCancel {
				vm.Interrupt("execution canceled")
				return
			}
			if f.kind != frameReply {
				vm.Interrupt("invalid parent response")
				return
			}
			select {
			case responses <- f:
			case <-done:
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if processCPU() >= CPUBudget {
					vm.Interrupt("1s CPU budget exceeded")
					_ = w.send(frameBudget, 0, nil, 0)
					return
				}
			case <-done:
				return
			}
		}
	}()
	pending := map[uint64]settlement{}
	var next uint64
	rpc := func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := vm.NewPromise()
		next++
		if next > uint64(l.Calls) {
			_ = reject("Host request budget exceeded")
			return vm.ToValue(promise)
		}
		kind, name, args := call.Argument(0).String(), call.Argument(1).String(), call.Argument(2).String()
		var data []byte
		var frameType byte
		if kind == "call" || kind == "search" {
			if len(name) > 1024 || len(args) > l.ArgumentBytes {
				_ = reject("Host argument budget exceeded")
				return vm.ToValue(promise)
			}
			data, err = json.Marshal(callRequest{Method: kind, Name: name, Arguments: json.RawMessage(args)})
			frameType = frameCall
		} else {
			err = fmt.Errorf("invalid bridge operation")
		}
		if err == nil {
			err = w.send(frameType, next, data, l.ArgumentBytes+2048)
		}
		if err != nil {
			vm.Interrupt("Host transport failed")
			_ = reject("Host transport failed")
		} else {
			pending[next] = settlement{resolve, reject}
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
	lraw, _ := json.Marshal(l)
	// JSON.parse creates plain JS data; no Go Limits/Host/Session is reflected.
	limitsValue, err := vm.RunString("JSON.parse(" + fmt.Sprintf("%q", string(lraw)) + ")")
	if err != nil {
		return 2
	}
	namesValue, err := vm.RunString("JSON.parse(" + fmt.Sprintf("%q", namesJSON) + ")")
	if err != nil {
		return 2
	}
	bridge, err := build(goja.Undefined(), vm.ToValue(rpc), limitsValue, namesValue)
	if err != nil {
		return 2
	}
	if err = vm.Set("tools", bridge.ToObject(vm).Get("tools")); err != nil {
		return 2
	}
	encode, ok := goja.AssertFunction(bridge.ToObject(vm).Get("encode"))
	if !ok {
		return 2
	}
	if w.send(frameReady, 0, nil, 0) != nil {
		return 2
	}
	sourceName := cfg.SourceName
	if sourceName == "" {
		sourceName = "inline.js"
	}
	value, runErr := vm.RunScript(sourceName, "(async function(){\"use strict\";"+code+"\n})()")
	if runErr != nil {
		return workerError(w, l.OutputBytes, runErr)
	}
	promise, ok := value.Export().(*goja.Promise)
	if !ok {
		return workerError(w, l.OutputBytes, fmt.Errorf("missing async result"))
	}
	for promise.State() == goja.PromiseStatePending {
		if len(pending) == 0 {
			return workerError(w, l.OutputBytes, fmt.Errorf("unsettled Promise without a Host producer"))
		}
		select {
		case f := <-responses:
			one, ok := pending[f.id]
			if !ok {
				return workerError(w, l.OutputBytes, fmt.Errorf("unknown Host response ID"))
			}
			delete(pending, f.id)
			// resolve runs Promise jobs synchronously on this owner, never on
			// a Host, pipe-reader or watchdog goroutine.
			if err := one.resolve(string(f.data)); err != nil {
				return workerError(w, l.OutputBytes, err)
			}
		case <-time.After(10 * time.Millisecond):
			// An idle VM still needs to observe cancellation/CPU interrupts.
			if _, err := vm.RunString("void 0"); err != nil {
				return workerError(w, l.OutputBytes, err)
			}
		}
	}
	if promise.State() == goja.PromiseStateRejected {
		message := promise.Result().String()
		if object, ok := promise.Result().(*goja.Object); ok {
			if stack := object.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
				for _, line := range strings.Split(stack.String(), "\n") {
					if strings.Contains(line, sourceName+":") {
						tail := line[strings.Index(line, sourceName+":")+len(sourceName)+1:]
						end := strings.IndexByte(tail, ':')
						if end > 0 {
							message += "\n" + sourceName + ":" + tail[:end]
						}
						break
					}
				}
			}
		}
		return workerError(w, l.OutputBytes, fmt.Errorf("%s", message))
	}
	encoded, err := encode(goja.Undefined(), promise.Result())
	if err != nil {
		return workerError(w, l.OutputBytes, err)
	}
	if len(pending) != 0 {
		return workerError(w, l.OutputBytes, fmt.Errorf("script returned with unawaited Host requests"))
	}
	if err = w.send(frameResult, 0, []byte(encoded.String()), l.OutputBytes); err != nil {
		return 2
	}
	return 0
}

func workerError(w *wireWriter, budget int, err error) int {
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
	return 1
}
