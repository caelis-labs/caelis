package gatewayapp

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/bot"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotWorkNativeCredentialAndPathCeiling(t *testing.T) {
	if os.Getenv("CAELIS_TEST_BOT_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_BOT_NATIVE=1 for native managed work isolation")
	}
	ctx := t.Context()
	store := t.TempDir()
	id := bot.Identity("owner", "create")
	files, err := bot.NewWorkFiles(store, id, bot.WorkID(id, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Init(ctx); err != nil {
		t.Fatal(err)
	}
	rt, err := newBotWorkRuntime(ctx, files, store)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(store, "host-token")
	if err := os.WriteFile(secret, []byte("HOST_SECRET_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_TEST_CREDENTIAL", "ENV_SECRET_SENTINEL")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	run := func(command string) sandbox.CommandResult {
		t.Helper()
		out, err := rt.Run(ctx, sandbox.CommandRequest{Command: command, Constraints: sandbox.Constraints{Route: sandbox.RouteHost, Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run("printf own > result.txt; cat result.txt; printf '%s' \"$BOT_TEST_CREDENTIAL\"")
	if !strings.Contains(out.Stdout, "own") || strings.Contains(out.Stdout, "ENV_SECRET_SENTINEL") {
		t.Fatalf("own work/environment: %+v", out)
	}
	out = run("if cat " + quote(secret) + "; then printf ESCAPED; else printf DENIED; fi")
	if !strings.Contains(out.Stdout, "DENIED") || strings.Contains(out.Stdout, "HOST_SECRET_SENTINEL") {
		t.Fatalf("Host read escaped: %+v", out)
	}
	out = run("if printf changed > " + quote(secret) + "; then printf ESCAPED; else printf DENIED; fi")
	if !strings.Contains(out.Stdout, "DENIED") {
		t.Fatal("Host write escaped")
	}
	if raw, err := os.ReadFile(secret); err != nil || string(raw) != "HOST_SECRET_SENTINEL" {
		t.Fatalf("Host file changed: %s %v", raw, err)
	}
	if _, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: "pwd", Dir: store}); err == nil {
		t.Fatal("caller selected outside cwd")
	}
}
