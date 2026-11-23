package sessiontest_test

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/sessiontest"
)

func TestFileToolRunConformance(t *testing.T) {
	cfg := file.Config{RootDir: t.TempDir()}
	active, err := file.NewStore(cfg).StartSession(t.Context(), session.StartSessionRequest{AppName: "conformance", UserID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	sessiontest.ToolRunConformance(t, active.SessionRef, func(*testing.T) session.RunStore {
		store := file.NewStore(cfg)
		return struct{ session.RunStore }{store}
	})
}
