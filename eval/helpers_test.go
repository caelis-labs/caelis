//go:build e2e

package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
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

type recordingStreams struct {
	frames []stream.Frame
}

func (s *recordingStreams) PublishStream(frame stream.Frame) {
	s.frames = append(s.frames, stream.CloneFrame(frame))
}
