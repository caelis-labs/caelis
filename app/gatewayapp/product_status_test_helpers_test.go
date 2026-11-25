package gatewayapp_test

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"
)

func builtinComputerUseSupported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" ||
		runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") ||
		runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
}

func builtinComputerUseVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../control/plugin/builtin/desktop-world.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pin); err != nil {
		t.Fatal(err)
	}
	return pin.Version
}
