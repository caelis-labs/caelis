package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestValidateStoreRootChecksTransactionVersionWithoutRepair(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "preflight-session" }})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "preflight-user"})
	if err != nil {
		t.Fatal(err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	transaction := map[string]any{
		"kind":     transactionKind,
		"version":  transactionVersion + 1,
		"document": json.RawMessage(document),
		"events":   []any{},
	}
	raw, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	transactionPath := documentPath + transactionSuffix
	if err := os.WriteFile(transactionPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateStoreRoot(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unsupported Session transaction %q version %d", transactionKind, transactionVersion+1)) {
		t.Fatalf("ValidateStoreRoot() error = %v, want future transaction rejection", err)
	}
	after, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("ValidateStoreRoot() changed the Session document")
	}
}
