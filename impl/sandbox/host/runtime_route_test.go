package host

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/sandbox"
)

func TestRuntimeReportsActualHostRoute(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := rt.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo ok",
		Constraints: sandbox.Constraints{
			Route:   sandbox.RouteSandbox,
			Backend: sandbox.BackendWindowsElevated,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v; stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("stdout = %q, want ok", result.Stdout)
	}
	if result.Route != sandbox.RouteHost || result.Backend != sandbox.BackendHost {
		t.Fatalf("result route/backend = %q/%q, want host/host", result.Route, result.Backend)
	}
}
