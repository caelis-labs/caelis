package runnertrace

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var enabled atomic.Bool
var sinkMu sync.Mutex
var sink func(string)

func Enabled() bool {
	return enabled.Load()
}

func SetEnabled(value bool) func() {
	previous := enabled.Swap(value)
	return func() {
		enabled.Store(previous)
	}
}

func SetSink(fn func(string)) func() {
	sinkMu.Lock()
	previous := sink
	sink = fn
	sinkMu.Unlock()
	return func() {
		sinkMu.Lock()
		sink = previous
		sinkMu.Unlock()
	}
}

func Span(component, name string) func() {
	if !Enabled() {
		return func() {}
	}
	started := time.Now()
	Printf(component, "%s start", name)
	return func() {
		Printf(component, "%s done duration=%s", name, time.Since(started).Round(time.Millisecond))
	}
}

func Printf(component, format string, args ...any) {
	if !Enabled() {
		return
	}
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("caelis-runner-trace %s ts=%s pid=%d %s\n", component, time.Now().Format(time.RFC3339Nano), os.Getpid(), message)
	writeLine(line)
}

func Emit(line string) {
	if !Enabled() {
		return
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	writeLine(line + "\n")
}

func writeLine(line string) {
	sinkMu.Lock()
	fn := sink
	sinkMu.Unlock()
	if fn != nil {
		fn(line)
	}
}
