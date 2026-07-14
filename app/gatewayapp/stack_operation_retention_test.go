package gatewayapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
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
			intent := internalcontrolclient.OperationIntent{
				PrincipalID: "owner",
				OperationID: "assembly-retention",
				Action:      controlport.ActionPrompt,
				SessionID:   "session-1",
				Target:      "session-1",
				Digest:      "digest",
			}
			record, created, err := stack.operations.Begin(context.Background(), intent)
			if err != nil || !created || time.Duration(record.TerminalRetentionNanoseconds) != test.want {
				t.Fatalf("Begin() = %#v, created %v, error %v; want retention %v", record, created, err, test.want)
			}
			self, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, "self")
			if !ok {
				t.Fatalf("self agent missing from assembly: %#v", stack.runtime.Assembly.Agents)
			}
			if got, ok := argValue(self.Args, "-control-operation-retention"); !ok || got != test.want.String() {
				t.Fatalf("self retention arg = %q (present %v), want %q in %#v", got, ok, test.want, self.Args)
			}
		})
	}
}

func TestStackAdoptsExistingControlOperationRetentionForSelfACP(t *testing.T) {
	storeDir := t.TempDir()
	operationRoot := filepath.Join(storeDir, "control-operations")
	seed, err := internalcontrolclient.NewFileOperationStoreWithConfig(
		operationRoot,
		internalcontrolclient.OperationRetentionConfig{TerminalRetention: 6 * time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	self, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, "self")
	if !ok {
		t.Fatalf("self agent missing from assembly: %#v", stack.runtime.Assembly.Agents)
	}
	if got, ok := argValue(self.Args, "-control-operation-retention"); !ok || got != (6*time.Hour).String() {
		t.Fatalf("self retention arg = %q (present %v), want %q", got, ok, 6*time.Hour)
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
