package local

import (
	"testing"

	sessionfile "github.com/caelis-labs/caelis/impl/session/file"
	taskapi "github.com/caelis-labs/caelis/ports/task"
)

func newFileTaskStoreForTest(t testing.TB) taskapi.Store {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	return sessionfile.NewTaskStore(store)
}
