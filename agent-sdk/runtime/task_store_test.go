package runtime

import (
	"testing"

	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func newFileTaskStoreForTest(t testing.TB) taskapi.Store {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	return sessionfile.NewTaskStore(store)
}
