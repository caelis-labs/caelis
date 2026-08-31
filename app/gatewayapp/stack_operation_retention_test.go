package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestStackAssemblesConfiguredControlOperationRetention(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_OPERATION_RETENTION", "1h")
	for _, test := range []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{name: "default", want: DefaultControlOperationRetention},
		{name: "custom", configured: 48 * time.Hour, want: 48 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			stack, err := newGatewayAppTestStack(t, Config{
				StoreDir:                  t.TempDir(),
				ControlOperationRetention: test.configured,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stack.Close()
			intent := appserver.OperationIntent{
				PrincipalID: "owner",
				OperationID: "assembly-retention",
				Action:      appserver.ActionPrompt,
				SessionID:   "session-1",
				Target:      "session-1",
				Digest:      "digest",
			}
			record, created, err := stack.operations.Begin(context.Background(), intent)
			if err != nil || !created || time.Duration(record.TerminalRetentionNanoseconds) != test.want {
				t.Fatalf("Begin() = %#v, created %v, error %v; want retention %v", record, created, err, test.want)
			}
		})
	}
}

func TestStackStartsFreshWithoutImportingRetiredControlState(t *testing.T) {
	storeDir := t.TempDir()
	operationRoot := filepath.Join(storeDir, "control-operations")
	seed, err := appserver.NewFileOperationStoreWithConfig(
		operationRoot,
		appserver.OperationRetentionConfig{TerminalRetention: 6 * time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyIntent := appserver.OperationIntent{
		PrincipalID: "owner", OperationID: "retired-operation", Action: appserver.ActionPluginInstall,
		Target: "demo", Digest: "retired-digest",
	}
	if _, created, err := seed.Begin(context.Background(), legacyIntent); err != nil || !created {
		t.Fatalf("seed legacy operation = created %v, error %v", created, err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operationRoot, ".retention-policy.json"), []byte("not-json"), 0o000); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(storeDir, "acp-preparations"),
		filepath.Join(storeDir, "plugins", "operation-receipts"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("retired and intentionally unreadable as a directory"), 0o000); err != nil {
			t.Fatal(err)
		}
	}

	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	if stack.controlOperationRetention != DefaultControlOperationRetention {
		t.Fatalf("Control operation retention = %v, want default %v", stack.controlOperationRetention, DefaultControlOperationRetention)
	}
	if _, created, err := stack.operations.Begin(context.Background(), legacyIntent); err != nil || !created {
		t.Fatalf("Begin(retired operation identity) = created %v, error %v; want fresh operation", created, err)
	}
	if _, err := os.Stat(operationRoot); err != nil {
		t.Fatalf("retired operation directory was not ignored in place: %v", err)
	}
}

func TestStackRejectsNegativeControlOperationRetention(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{
		StoreDir:                  t.TempDir(),
		ControlOperationRetention: -time.Hour,
	})
	if stack != nil {
		_ = stack.Close()
	}
	if err == nil {
		t.Fatal("NewLocalStack() accepted negative Control operation retention")
	}
}
