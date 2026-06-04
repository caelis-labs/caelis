//go:build e2e

package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	controladapter "github.com/OnslaughtSnail/caelis/app/gatewayapp/controladapter"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/controladapter/local"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func repoRootForEval(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}

func repoRootForGatewayAppTest(t *testing.T) string { return repoRootForEval(t) }
func repoRootForRunnerTest(t *testing.T) string     { return repoRootForEval(t) }

func newAdapterTestStack(t *testing.T, cfg gatewayapp.Config) (*gatewayapp.Stack, error) {
	t.Helper()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	return gatewayapp.NewLocalStack(cfg)
}

func newAdapterFromGatewayAppStack(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*controladapter.Adapter, error) {
	return local.NewLocalAdapter(ctx, stack, preferredSessionID, bindingKey, modelText)
}

func slashCandidatesHaveValue(candidates []controladapter.SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

type recordingStreams struct {
	frames []stream.Frame
}

func (s *recordingStreams) PublishStream(frame stream.Frame) {
	s.frames = append(s.frames, stream.CloneFrame(frame))
}
