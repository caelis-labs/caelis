package sdkctx

import "sync"

// TestHelper matches testing.TB without importing the testing package in
// production code.
type TestHelper interface {
	Helper()
	TempDir() string
	Cleanup(func())
}

var (
	logicalFileStoreMu sync.RWMutex
	logicalFileStoreFn func(any) bool
)

// RegisterLogicalFileStore registers the internal file store configurator used
// by internal SDK tests that cover logical persistence rather than crash sync.
func RegisterLogicalFileStore(fn func(any) bool) {
	logicalFileStoreMu.Lock()
	logicalFileStoreFn = fn
	logicalFileStoreMu.Unlock()
}

// UseLogicalFileStore disables expensive storage sync barriers on an internal
// test store instance. It must only be called from *_test.go files.
func UseLogicalFileStore[S any](t TestHelper, store S) S {
	if t == nil {
		panic("sdkctx.UseLogicalFileStore requires a non-nil TestHelper")
	}
	t.Helper()
	logicalFileStoreMu.RLock()
	fn := logicalFileStoreFn
	logicalFileStoreMu.RUnlock()
	if fn == nil || !fn(store) {
		panic("sdkctx.UseLogicalFileStore called before file store registration or with unsupported type")
	}
	return store
}
