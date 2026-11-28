// Package shellproc executes bounded Shell requests with native process cleanup.
package shellproc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

// WorkerArgument selects the private one-request stdio entry point.
const WorkerArgument = "--control-shell-worker"

const maxWorkerReply = 1 << 20

type workerRequest struct {
	Shell  appserver.ShellRequest
	Store  string
	Helper string
}

type workerReply struct {
	Result *appserver.ShellResult
	Code   errorcode.Code
}

// Supported reports platforms with native execution and process-tree cleanup.
func Supported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

// WorkerMain executes a single request. A panic, signal or fatal error exits
// this process and is observed by the supervising Control Host.
func WorkerMain(stdin io.Reader, stdout io.Writer) int {
	var req workerRequest
	if !Supported() || runtime.GOOS == "windows" || json.NewDecoder(io.LimitReader(stdin, 512<<10)).Decode(&req) != nil {
		return 2
	}
	result, err := execute(context.Background(), req.Shell, req.Store, req.Helper, "")
	reply := workerReply{}
	if err != nil {
		reply.Code = errorcode.CodeOf(err)
		if reply.Code == errorcode.Unknown {
			reply.Code = errorcode.Unavailable
		}
	} else {
		reply.Result = &result
	}
	if json.NewEncoder(stdout).Encode(reply) != nil {
		return 2
	}
	return 0
}

// Run executes one request with platform-owned process-tree cleanup. Unix uses
// a private worker process group, while Windows uses the SDK Job Object paths.
func Run(ctx context.Context, request appserver.ShellRequest, store, helper, authority string) (appserver.ShellResult, error) {
	if runtime.GOOS == "windows" {
		return execute(ctx, request, store, helper, authority)
	}
	var zero appserver.ShellResult
	executable, err := os.Executable()
	if err != nil {
		return zero, errorcode.Wrap(errorcode.Unavailable, "Shell worker executable is unavailable", err)
	}
	raw, err := json.Marshal(workerRequest{Shell: request, Store: store, Helper: helper})
	if err != nil {
		return zero, err
	}
	cmd := exec.CommandContext(ctx, executable, WorkerArgument)
	configureGroup(cmd)
	cmd.Cancel = func() error { return stopGroup(cmd) }
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOMAXPROCS=2", "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	cmd.Stdin = bytes.NewReader(raw)
	var reply replyBuffer
	cmd.Stdout = &reply
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, errorcode.Wrap(errorcode.Unavailable, "Shell worker could not start", err)
	}
	err = cmd.Wait()
	// The shell and native launchers inherit this group. This cleanup also runs
	// after a worker crash or after a successful shell leaves descendants.
	_ = stopGroup(cmd)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, errorcode.Wrap(errorcode.UnknownOutcome, "Shell worker exited without a result", err)
	}
	var response workerReply
	if err := json.Unmarshal(reply.Bytes(), &response); err != nil {
		return zero, errorcode.Wrap(errorcode.UnknownOutcome, "Shell worker produced an invalid result", err)
	}
	if response.Code != "" {
		message := "Shell execution failed"
		if response.Code == errorcode.InvalidArgument {
			message = "workspace must be an existing directory"
		}
		return zero, errorcode.New(response.Code, message)
	}
	if response.Result == nil {
		return zero, errorcode.New(errorcode.UnknownOutcome, "Shell worker did not return a result")
	}
	return *response.Result, nil
}

type replyBuffer struct{ bytes.Buffer }

func (b *replyBuffer) Write(p []byte) (int, error) {
	if len(p) > maxWorkerReply-b.Len() {
		return 0, errors.New("shell worker reply exceeded its byte limit")
	}
	return b.Buffer.Write(p)
}
