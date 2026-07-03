package local

import (
	"testing"

	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
)

func newFileTaskStoreForTest(t testing.TB) taskapi.Store {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	return sessionfile.NewTaskStore(store)
}
