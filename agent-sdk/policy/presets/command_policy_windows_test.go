//go:build windows

package presets

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
)

func TestPortableRootDeleteRemainsCatastrophicOnWindows(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("rm -rf /", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}
	if !strings.Contains(decision.Reason, "system or home root") {
		t.Fatalf("Reason = %q, want catastrophic root guidance", decision.Reason)
	}
}
