package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
)

func TestStartupFailureStatusIsBoundedAndStageSpecific(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&startupError{Stage: "initialize", Err: context.DeadlineExceeded}, "initialize: deadline exceeded"},
		{&startupError{Stage: "tools/list", Err: context.Canceled}, "tools/list: canceled"},
		{&startupError{Stage: "transport", Err: errors.New("token=private")}, "transport: failed"},
		{&startupError{Stage: "token=private", Err: errors.New("private")}, "startup: failed"},
	} {
		if got := startupFailureSummary(tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
	_, err := StartClient(t.Context(), ServerSpec{Name: "absent", Command: "/does-not-exist/private-token", WorkDir: t.TempDir()})
	var phase *startupError
	if !errors.As(err, &phase) || phase.Stage != "transport" {
		t.Fatalf("phase=%#v", phase)
	}
}

func TestManagerToolsListDeadlineKeepsStageWithoutPrivateCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failures := make(chan ServerFailure, 1)
		mgr, err := newManager(t.Context(), []ServerSpec{{Name: "slow", PluginID: "p"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			return asyncTestClient(ctx, spec, make(chan struct{}))
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		<-mgr.Initialized()
		f := <-failures
		var phase *startupError
		if !errors.As(f.Err, &phase) || phase.Stage != "tools/list" || !errors.Is(f.Err, context.DeadlineExceeded) {
			t.Fatalf("failure=%v", f.Err)
		}
		infos := mgr.GetServerInfos("p")
		if len(infos) != 1 || !strings.Contains(infos[0].Warning, "tools/list: deadline exceeded") {
			t.Fatalf("status=%#v", infos)
		}
	})
}
