package main

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestAgentSDKQuickstartImportsOnlySupportedPackages(t *testing.T) {
	t.Parallel()

	supported := map[string]bool{}
	allowlist, err := os.Open("../agent-sdk/supported-packages.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer allowlist.Close()
	scanner := bufio.NewScanner(allowlist)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line != "" {
			supported[line] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "../agent-sdk/runtime/quickstart_external_test.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(path, "github.com/caelis-labs/caelis/agent-sdk") && !supported[path] {
			t.Errorf("quickstart imports unsupported package %s", path)
		}
	}
}
