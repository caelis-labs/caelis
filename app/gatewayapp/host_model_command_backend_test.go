package gatewayapp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestHostModelCommandsUseHostCASAndSharedLedger(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	beforeSession, err := stack.composition.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connect := appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-connect", ExpectedRevision: &expected},
		Config: appserver.ConnectConfig{
			Provider: "ollama", Model: "command-model", BaseURL: "http://127.0.0.1:11434",
		},
	}
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, connect)
	if err != nil || connected.Outcome != appserver.OutcomeCommitted || connected.Revision != expected+1 {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	replayed, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, connect)
	if err != nil || replayed != connected {
		t.Fatalf("ConnectModel(replay) = %#v, %v; want %#v", replayed, err, connected)
	}
	changed := connect
	changed.Config.Model = "changed-model"
	conflicted, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, changed)
	if !errors.Is(err, appserver.ErrOperationConflict) || conflicted.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("ConnectModel(changed payload) = %#v, %v", conflicted, err)
	}
	stale, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-stale", ExpectedRevision: &expected},
		Model:     "ollama/command-model",
	})
	if err == nil || stale.Outcome != appserver.OutcomeConflicted || stale.Revision != connected.Revision || errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("UseModel(stale) = %#v, %v", stale, err)
	}

	fresh := connected.Revision
	used, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-use", ExpectedRevision: &fresh},
		Model:     "ollama/command-model",
	})
	if err != nil || used.Outcome != appserver.OutcomeCommitted || used.Revision != fresh+1 {
		t.Fatalf("UseModel() = %#v, %v", used, err)
	}
	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-delete", ExpectedRevision: &used.Revision},
		Model:     "ollama/command-model",
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted || deleted.Revision != used.Revision+1 {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, err)
	}
	afterSession, err := stack.composition.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if afterSession.Revision != beforeSession.Revision {
		t.Fatalf("Host model commands changed Session revision: before=%d after=%d", beforeSession.Revision, afterSession.Revision)
	}

	shared, err := stack.ControlClient().CreateSession(ctx, principal, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "shared-model-ledger"},
		PreferredSessionID: "shared-model-session",
		WorkspaceKey:       stack.composition.workspace.Key,
		CWD:                stack.composition.workspace.CWD,
	})
	if err != nil || shared.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession() = %#v, %v", shared, err)
	}
	sharedConflict, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "shared-model-ledger", ExpectedRevision: &deleted.Revision},
		Model:     stack.composition.lookup.DefaultID(),
	})
	if !errors.Is(err, appserver.ErrOperationConflict) || sharedConflict.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("UseModel(shared operation ID) = %#v, %v", sharedConflict, err)
	}
}

func TestHostModelConnectUsesCanonicalDocumentAndDoesNotPersistSecretInLedger(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
	doc, err := external.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.Runtime.ApprovalMode = "manual"
	doc.Runtime.PolicyProfile = "external-writer"
	saved, err := external.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	secret := "host-model-ledger-secret"
	request := appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-secret", ExpectedRevision: &saved.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "gpt-command", BaseURL: "https://models.example/v1", APIKey: secret,
		},
	}
	result, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != saved.ConfigurationRevision+1 {
		t.Fatalf("ConnectModel() = %#v, %v", result, err)
	}
	committed, err := external.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Runtime.ApprovalMode != "manual" || committed.Runtime.PolicyProfile != "external-writer" {
		t.Fatalf("Host model command lost canonical fields: %#v", committed)
	}
	if err := filepath.WalkDir(controlStoreRoot(stack.composition.authorities.storeDir), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("operation ledger %s contains plaintext API key", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostModelCredentialsUseStableEndpointReferenceAcrossPrincipals(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connect := func(principalID, secret string, expected uint64) appserver.CommandResult {
		t.Helper()
		result, connectErr := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: principalID}, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "shared-operation-id", ExpectedRevision: &expected},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "shared-model", BaseURL: "https://models.example/v1", APIKey: secret,
			},
		})
		if connectErr != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(%q) = %#v, %v", principalID, result, connectErr)
		}
		return result
	}
	firstSecret := "principal-one-secret"
	first := connect("principal-one", firstSecret, revision)
	afterFirst, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatalf("first persisted endpoint = %#v, %v", afterFirst.Models.ProviderEndpoints, err)
	}
	credentialRef := func(doc AppConfig) string {
		for _, endpoint := range doc.Models.ProviderEndpoints {
			if endpoint.Provider == "openai" && endpoint.BaseURL == "https://models.example/v1" {
				return endpoint.CredentialRef
			}
		}
		return ""
	}
	firstRef := credentialRef(afterFirst)
	firstSource, firstErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, firstRef)
	if firstRef == "" || firstErr != nil || firstSource.APIKey != firstSecret {
		t.Fatalf("first stable credential %q = %#v, %v", firstRef, firstSource, firstErr)
	}

	secondSecret := "principal-two-secret"
	second := connect("principal-two", secondSecret, first.Revision)
	afterSecond, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatalf("second persisted endpoint = %#v, %v", afterSecond.Models.ProviderEndpoints, err)
	}
	secondRef := credentialRef(afterSecond)
	if secondRef == "" || firstRef != secondRef {
		t.Fatalf("stable endpoint credential refs = %q / %q", firstRef, secondRef)
	}
	secondSource, secondErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, secondRef)
	if secondErr != nil || secondSource.APIKey != secondSecret {
		t.Fatalf("replacement credential = %#v, %v", secondSource, secondErr)
	}
	if second.Revision != first.Revision+1 {
		t.Fatalf("second revision = %d, want %d", second.Revision, first.Revision+1)
	}
}

func TestHostModelConnectReusableAuthUsesCanonicalSnapshot(t *testing.T) {
	t.Run("canonical endpoint without credential rejects retained stable fallback", func(t *testing.T) {
		ctx := context.Background()
		stack, _ := newLocalStateTestStack(t)
		principal := appserver.Principal{ID: stack.composition.authorities.userID}
		revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "canonical-auth-connect", ExpectedRevision: &revision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "canonical-auth", BaseURL: "https://models.example/v1", APIKey: "canonical-secret",
			},
		})
		if err != nil || connected.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(initial) = %#v, %v", connected, err)
		}
		external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
		doc, err := external.LoadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for index := range doc.Models.ProviderEndpoints {
			if doc.Models.ProviderEndpoints[index].Provider == "openai" {
				doc.Models.ProviderEndpoints[index].CredentialRef = ""
			}
		}
		saved, err := external.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
		if err != nil {
			t.Fatal(err)
		}
		result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "canonical-auth-reject", ExpectedRevision: &saved.ConfigurationRevision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "must-require-key", BaseURL: "https://models.example/v1",
			},
		})
		if commandErr == nil || result.Outcome != appserver.OutcomeRejected || !strings.Contains(commandErr.Error(), "API key") {
			t.Fatalf("ConnectModel(stale live auth) = %#v, %v", result, commandErr)
		}
		actual, revisionErr := stack.ControlStatus().ConfigurationRevision(ctx)
		if revisionErr != nil || actual != saved.ConfigurationRevision {
			t.Fatalf("revision after rejected stale auth = %d, %v; want %d", actual, revisionErr, saved.ConfigurationRevision)
		}
	})

	t.Run("canonical legacy credential reference takes precedence over retained stable reference", func(t *testing.T) {
		ctx := context.Background()
		stack, _ := newLocalStateTestStack(t)
		principal := appserver.Principal{ID: stack.composition.authorities.userID}
		revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		baseURL := "https://legacy-ref.example/v1"
		connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "legacy-ref-seed", ExpectedRevision: &revision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "legacy-seed", BaseURL: baseURL, APIKey: "stable-secret",
			},
		})
		if err != nil || connected.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(seed) = %#v, %v", connected, err)
		}
		legacyRef := "apikey:test:shared"
		if err := stack.composition.authorities.apiKeyCredentials.Put(ctx, legacyRef, "legacy-secret"); err != nil {
			t.Fatal(err)
		}
		external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
		doc, err := external.LoadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for index := range doc.Models.ProviderEndpoints {
			if doc.Models.ProviderEndpoints[index].Provider == "openai" &&
				modelconfig.NormalizeBaseURL(doc.Models.ProviderEndpoints[index].BaseURL) == modelconfig.NormalizeBaseURL(baseURL) {
				doc.Models.ProviderEndpoints[index].CredentialRef = legacyRef
			}
		}
		saved, err := external.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
		if err != nil {
			t.Fatal(err)
		}
		stableRef := retainedAPIKeyReference("openai", "", baseURL)
		if err := stack.composition.authorities.apiKeyCredentials.Delete(ctx, stableRef); err != nil {
			t.Fatal(err)
		}
		result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "legacy-ref-reuse", ExpectedRevision: &saved.ConfigurationRevision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "legacy-reused", BaseURL: baseURL,
			},
		})
		if commandErr != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(reuse legacy ref) = %#v, %v", result, commandErr)
		}
		persisted, err := stack.composition.authorities.store.LoadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range persisted.Models.ProviderEndpoints {
			if endpoint.Provider == "openai" && modelconfig.NormalizeBaseURL(endpoint.BaseURL) == modelconfig.NormalizeBaseURL(baseURL) {
				if endpoint.CredentialRef != legacyRef {
					t.Fatalf("reused endpoint credential = %q, want canonical legacy ref %q", endpoint.CredentialRef, legacyRef)
				}
				return
			}
		}
		t.Fatalf("reused endpoint missing: %#v", persisted.Models.ProviderEndpoints)
	})

	t.Run("canonical credential permits stale empty live lookup", func(t *testing.T) {
		ctx := context.Background()
		stack, _ := newLocalStateTestStack(t)
		principal := appserver.Principal{ID: stack.composition.authorities.userID}
		before := stack.composition.lookup.Snapshot()
		contextWindow := stack.composition.lookup.contextWindow
		revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "canonical-auth-seed", ExpectedRevision: &revision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "seed", BaseURL: "https://models.example/v1", APIKey: "canonical-secret",
			},
		})
		if err != nil || connected.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(seed) = %#v, %v", connected, err)
		}
		stack.composition.lookup.Restore(before, contextWindow)
		result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "canonical-auth-reuse", ExpectedRevision: &connected.Revision},
			Config: appserver.ConnectConfig{
				Provider: "openai", Model: "reused", BaseURL: "https://models.example/v1",
			},
		})
		if commandErr != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != connected.Revision+1 {
			t.Fatalf("ConnectModel(canonical reusable auth) = %#v, %v", result, commandErr)
		}
		persisted, err := stack.composition.authorities.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range persisted.Models.ProviderEndpoints {
			if endpoint.Provider == "openai" && endpoint.CredentialRef != "" {
				return
			}
		}
		t.Fatalf("canonical reusable credential disappeared: %#v", persisted.Models.ProviderEndpoints)
	})
}

func TestHostModelReusableCredentialsAreScopedToExactEndpointIdentity(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://shared-endpoint.example/v1"
	connect := func(operationID, endpointID, model, apiKey string, expected uint64) appserver.CommandResult {
		t.Helper()
		result, connectErr := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: operationID, ExpectedRevision: &expected},
			Config: appserver.ConnectConfig{
				Provider: "openai", EndpointID: endpointID, Model: model, BaseURL: baseURL, APIKey: apiKey,
			},
		})
		if connectErr != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(%q) = %#v, %v", endpointID, result, connectErr)
		}
		return result
	}
	first := connect("endpoint-a-connect", "tenant-a", "tenant-a-model", "tenant-a-secret", revision)
	second := connect("endpoint-b-connect", "tenant-b", "tenant-b-model", "tenant-b-secret", first.Revision)
	reused := connect("endpoint-b-reuse", "tenant-b", "tenant-b-next", "", second.Revision)
	if reused.Revision != second.Revision+1 {
		t.Fatalf("reused endpoint revision = %d, want %d", reused.Revision, second.Revision+1)
	}
	refA := retainedAPIKeyReference("openai", "tenant-a", baseURL)
	refB := retainedAPIKeyReference("openai", "tenant-b", baseURL)
	if refA == refB {
		t.Fatalf("endpoint credential refs collided: %q", refA)
	}
	for ref, want := range map[string]string{refA: "tenant-a-secret", refB: "tenant-b-secret"} {
		if source, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); lookupErr != nil || source.APIKey != want {
			t.Fatalf("endpoint credential %q = %#v, %v; want %q", ref, source, lookupErr, want)
		}
	}
	persisted, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundTenantB := false
	for _, configured := range persisted.Models.Configs {
		if configured.Model == "tenant-b-next" {
			foundTenantB = configured.ProviderEndpointID == retainedProviderEndpointID("openai", "tenant-b", baseURL)
		}
	}
	if !foundTenantB {
		t.Fatalf("tenant-b reuse bound the wrong endpoint: %#v", persisted.Models.Configs)
	}
}

func TestHostModelDeleteRetiresCredentialAfterLastProfileWithoutInterruptingPinnedRuntime(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://retired.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retired-auth-connect", ExpectedRevision: &revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "pinned-model,sibling-model", BaseURL: baseURL, APIKey: "retired-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	pinnedDoc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := cloneSessionModelLookup(stack.composition.lookup, pinnedDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := pinned.pinAPIKeyCredentials(ctx, providerProfileAPIKeyCredentialRefs(pinnedDoc)); err != nil {
		t.Fatal(err)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)

	firstDelete, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retired-auth-delete-first", ExpectedRevision: &connected.Revision},
		Model:     "openai/pinned-model",
	})
	if err != nil || firstDelete.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel(first profile) = %#v, %v", firstDelete, err)
	}
	if source, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); lookupErr != nil || source.APIKey != "retired-secret" {
		t.Fatalf("shared endpoint credential after first delete = %#v, %v", source, lookupErr)
	}
	if !stack.Models().HasReusableAuth(ctx, "openai", baseURL) {
		t.Fatal("shared endpoint credential is not reusable while one ModelProfile remains")
	}

	lastDelete, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retired-auth-delete-last", ExpectedRevision: &firstDelete.Revision},
		Model:     "openai/sibling-model",
	})
	if err != nil || lastDelete.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel(last profile) = %#v, %v", lastDelete, err)
	}
	if _, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); !errors.Is(lookupErr, os.ErrNotExist) {
		t.Fatalf("retired endpoint credential lookup error = %v, want os.ErrNotExist", lookupErr)
	}
	if stack.Models().HasReusableAuth(ctx, "openai", baseURL) {
		t.Fatal("deleted endpoint credential remains reusable")
	}
	for _, alias := range []string{"openai/pinned-model", "openai/sibling-model"} {
		if _, resolveErr := pinned.ResolveModel(ctx, alias, 0); resolveErr != nil {
			t.Fatalf("pinned Runtime generation lost %q credential: %v", alias, resolveErr)
		}
	}

	reconnected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retired-auth-reconnect", ExpectedRevision: &lastDelete.Revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "must-require-key", BaseURL: baseURL,
		},
	})
	if err == nil || reconnected.Outcome != appserver.OutcomeRejected || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("ConnectModel(without retired key) = %#v, %v", reconnected, err)
	}
}

func TestHostModelDeleteRestoresCredentialWhenConfigurationSaveFails(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://retirement-rollback.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retirement-rollback-connect", ExpectedRevision: &revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "rollback-model", BaseURL: baseURL, APIKey: "accepted-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	failure := errors.New("delete config save failed")
	stack.composition.authorities.store.saveHook = func(AppConfig) error { return failure }
	deleted, deleteErr := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retirement-rollback-delete", ExpectedRevision: &connected.Revision},
		Model:     "openai/rollback-model",
	})
	if !errors.Is(deleteErr, failure) || deleted.Outcome != appserver.OutcomeRejected {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, deleteErr)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)
	if source, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); lookupErr != nil || source.APIKey != "accepted-secret" {
		t.Fatalf("credential after delete rollback = %#v, %v", source, lookupErr)
	}
	if !stack.composition.lookup.HasAlias("openai/rollback-model") {
		t.Fatal("failed delete removed model from live catalog")
	}
}

func TestHostModelDeleteRetiresCredentialAfterLastReachableProfile(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://orphan-model.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-model-connect", ExpectedRevision: &revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "reachable-model,orphan-model", BaseURL: baseURL, APIKey: "orphan-model-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}

	external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
	doc, err := external.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orphanConfig, err := newModelLookupFromDocument(doc, 0)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := orphanConfig.ResolveConfig("openai/orphan-model")
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, modelprofile.BuildProviderID(orphan.ID))
	saved, err := external.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-model-delete-last-profile", ExpectedRevision: &saved.ConfigurationRevision},
		Model:     "openai/reachable-model",
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel(last reachable profile) = %#v, %v", deleted, err)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)
	if _, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); !errors.Is(lookupErr, os.ErrNotExist) {
		t.Fatalf("orphan endpoint credential lookup error = %v, want os.ErrNotExist", lookupErr)
	}
	if stack.Models().HasReusableAuth(ctx, "openai", baseURL) {
		t.Fatal("orphan model kept endpoint credential reusable")
	}
	reconnected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-model-reconnect", ExpectedRevision: &deleted.Revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "must-require-key", BaseURL: baseURL,
		},
	})
	if err == nil || reconnected.Outcome != appserver.OutcomeRejected || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("ConnectModel(with orphan endpoint) = %#v, %v", reconnected, err)
	}
}

func TestHostModelCommandPreCanceledContextHasNoEffect(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	before, err := stack.ControlStatus().ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-cancelled", ExpectedRevision: &before},
		Config:    appserver.ConnectConfig{Provider: "openai", Model: "cancelled", APIKey: "must-not-write"},
	})
	if err == nil || result.Outcome == appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(cancelled) = %#v, %v", result, err)
	}
	after, loadErr := stack.ControlStatus().ConfigurationRevision(context.Background())
	if loadErr != nil || after != before {
		t.Fatalf("cancelled model revision = %d, %v; want %d", after, loadErr, before)
	}
}

func TestHostModelConnectCommitsWhileTurnIsActiveWithoutReplacingRuntime(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	blocking := &blockingRuntime{session: active, release: make(chan struct{})}
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack.composition.mu.Lock()
	stack.composition.gateway = gateway
	stack.composition.mu.Unlock()
	handle, err := gateway.BeginTurn(ctx, kernelimpl.BeginTurnRequest{
		SessionRef: active.SessionRef,
		Input:      "hold configuration guard",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Handle.Close()
	defer close(blocking.release)

	before, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeGateway := stack.composition.currentGateway()
	result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-active-turn", ExpectedRevision: &before},
		Config:    appserver.ConnectConfig{Provider: "ollama", Model: "future-model", BaseURL: "http://127.0.0.1:11434"},
	})
	if commandErr != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != before+1 {
		t.Fatalf("ConnectModel(active turn) = %#v, %v", result, commandErr)
	}
	after, revisionErr := stack.ControlStatus().ConfigurationRevision(ctx)
	if revisionErr != nil || after != before+1 {
		t.Fatalf("configuration revision after mutation = %d, %v; want %d", after, revisionErr, before+1)
	}
	if stack.composition.currentGateway() != beforeGateway {
		t.Fatal("Host model mutation replaced the active Runtime")
	}
}

func TestHostModelConnectRejectsConcurrentOAuthWithoutSecondEffect(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	expected, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release, err := stack.commandBackend.beginHostModelAuthentication("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	credentialPath := filepath.Join(stack.composition.authorities.storeDir, "providers", "codex", "auth.json")
	result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-concurrent-oauth", ExpectedRevision: &expected},
		Config:    appserver.ConnectConfig{Provider: "codex", Model: "gpt-5.6-sol"},
	})
	if commandErr == nil || result.Outcome != appserver.OutcomeConflicted || errorcode.CodeOf(commandErr) != errorcode.Conflict {
		t.Fatalf("ConnectModel(concurrent OAuth) = %#v, %v", result, commandErr)
	}
	after, revisionErr := stack.ControlStatus().ConfigurationRevision(ctx)
	if revisionErr != nil || after != expected {
		t.Fatalf("configuration revision after rejected OAuth = %d, %v; want %d", after, revisionErr, expected)
	}
	if _, statErr := os.Stat(credentialPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("concurrent OAuth created credential effect: %v", statErr)
	}
}

func TestHostModelCommandRollsForwardAfterCommittedWriteWarning(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	fault := errors.New("directory fsync after model CAS failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	expected, err := stack.ControlStatus().ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-committed-write", ExpectedRevision: &expected},
		Config: appserver.ConnectConfig{
			Provider: "ollama", Model: "committed-model", BaseURL: "http://127.0.0.1:11434",
		},
	}
	result, err := stack.ConfigurationCommands().ConnectModel(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != expected+1 ||
		!strings.Contains(result.Detail, fault.Error()) {
		t.Fatalf("ConnectModel(committed warning) = %#v, %v", result, err)
	}
	if !stack.composition.lookup.HasAlias("ollama/committed-model") {
		t.Fatal("committed model was not rolled forward into the live lookup")
	}
	replayed, replayErr := stack.ConfigurationCommands().ConnectModel(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if replayErr != nil || replayed != result || writeCount() != 1 {
		t.Fatalf("ConnectModel(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, writeCount(), result)
	}
}

func TestHostModelCommandPersistsUnknownWhenCredentialRollbackIsIncomplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode fault injection is Unix-only")
	}
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	expected, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	credentialRoot := filepath.Join(stack.composition.authorities.storeDir, "providers", "credentials")
	configWrites := 0
	wantErr := errors.New("model config precommit failed")
	stack.composition.authorities.store.saveHook = func(AppConfig) error {
		configWrites++
		if err := os.Chmod(credentialRoot, 0o500); err != nil {
			return errors.Join(wantErr, err)
		}
		return wantErr
	}
	permissionsRestored := false
	restorePermissions := func() {
		if permissionsRestored {
			return
		}
		if err := os.Chmod(credentialRoot, 0o700); err != nil {
			t.Fatalf("restore credential directory permissions: %v", err)
		}
		permissionsRestored = true
	}
	t.Cleanup(restorePermissions)
	request := appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-rollback-incomplete", ExpectedRevision: &expected},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "rollback-incomplete", BaseURL: "https://rollback.example/v1", APIKey: "uncommitted-secret",
		},
	}
	result, commandErr := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if !errors.Is(commandErr, wantErr) || !errors.Is(commandErr, credentialstore.ErrRollbackIncomplete) ||
		result.Outcome != appserver.OutcomeUnknown || result.ErrorCode != errorcode.UnknownOutcome {
		t.Fatalf("ConnectModel(incomplete rollback) = %#v, %v", result, commandErr)
	}
	restorePermissions()
	actual, revisionErr := stack.ControlStatus().ConfigurationRevision(ctx)
	if revisionErr != nil || actual != expected {
		t.Fatalf("configuration revision after incomplete rollback = %d, %v; want %d", actual, revisionErr, expected)
	}
	replayed, replayErr := stack.ConfigurationCommands().ConnectModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if replayErr != nil || replayed != result || configWrites != 1 {
		t.Fatalf("ConnectModel(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, configWrites, result)
	}
}

func TestHostModelCommandDoesNotGuessRevisionWhenCommittedWriteCannotBeReadBack(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	fault := errors.New("directory fsync after model CAS failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.composition.authorities.store.saveHook
	committedPath := stack.composition.authorities.store.path + ".committed-model"
	pathBlocked := false
	restore := func() {
		if !pathBlocked {
			return
		}
		if err := os.Remove(stack.composition.authorities.store.path); err != nil {
			t.Fatalf("remove blocked config path: %v", err)
		}
		if err := os.Rename(committedPath, stack.composition.authorities.store.path); err != nil {
			t.Fatalf("restore committed config: %v", err)
		}
		pathBlocked = false
	}
	t.Cleanup(restore)
	stack.composition.authorities.store.saveHook = func(doc AppConfig) error {
		committedErr := committedFault(doc)
		if !configstore.WriteCommitted(committedErr) {
			return committedErr
		}
		if err := os.Rename(stack.composition.authorities.store.path, committedPath); err != nil {
			return errors.Join(committedErr, err)
		}
		if err := os.Mkdir(stack.composition.authorities.store.path, 0o700); err != nil {
			_ = os.Rename(committedPath, stack.composition.authorities.store.path)
			return errors.Join(committedErr, err)
		}
		pathBlocked = true
		return committedErr
	}
	expected, err := stack.ControlStatus().ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secret := "committed-model-secret"
	request := appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-committed-readback", ExpectedRevision: &expected},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "readback-model", BaseURL: "https://models.example/v1", APIKey: secret,
		},
	}
	result, err := stack.ConfigurationCommands().ConnectModel(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if !errors.Is(err, fault) || result.Outcome != appserver.OutcomeUnknown || result.Revision != 0 {
		t.Fatalf("ConnectModel(committed readback failure) = %#v, %v", result, err)
	}
	if stack.composition.lookup.HasAlias("openai/readback-model") {
		t.Fatal("live lookup installed an unobserved candidate while canonical readback was unavailable")
	}
	restore()
	persisted, loadErr := stack.composition.authorities.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.ConfigurationRevision != expected+1 || len(persisted.Models.ProviderEndpoints) == 0 {
		t.Fatalf("persisted model configuration = %#v", persisted)
	}
	credentialRef := persisted.Models.ProviderEndpoints[len(persisted.Models.ProviderEndpoints)-1].CredentialRef
	source, credentialErr := stack.composition.authorities.apiKeyCredentials.LookupSource(context.Background(), credentialRef)
	if credentialErr != nil || source.APIKey != secret {
		t.Fatalf("committed credential %q = %#v, %v", credentialRef, source, credentialErr)
	}
	replayed, replayErr := stack.ConfigurationCommands().ConnectModel(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if replayErr != nil || replayed != result || writeCount() != 1 {
		t.Fatalf("ConnectModel(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, writeCount(), result)
	}
}

func TestHostModelCommandReconcilesNewerCanonicalRevisionBeforeLiveInstall(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connect := func(operationID, model string, expected uint64) appserver.CommandResult {
		t.Helper()
		result, connectErr := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: operationID, ExpectedRevision: &expected},
			Config: appserver.ConnectConfig{
				Provider: "ollama", Model: model, BaseURL: "http://127.0.0.1:11434",
			},
		})
		if connectErr != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("ConnectModel(%q) = %#v, %v", model, result, connectErr)
		}
		return result
	}
	first := connect("reconcile-connect-first", "reconcile-first", revision)
	second := connect("reconcile-connect-second", "reconcile-second", first.Revision)
	external := newAppConfigStore(filepath.Dir(stack.composition.authorities.store.path))
	stack.composition.authorities.store.savedHook = func() {
		stack.composition.authorities.store.savedHook = nil
		doc, loadErr := external.LoadContext(ctx)
		if loadErr != nil {
			t.Errorf("external LoadContext: %v", loadErr)
			return
		}
		externalProfileID := ""
		for _, profile := range doc.ModelProfiles.Profiles {
			if profile.Backend.Provider != nil && strings.Contains(profile.Backend.Provider.ModelConfigID, "reconcile-second") {
				externalProfileID = profile.ID
				break
			}
		}
		doc.ModelProfiles, loadErr = modelprofile.SelectDefault(doc.ModelProfiles, externalProfileID, "")
		if loadErr != nil {
			t.Errorf("external SelectDefault: %v", loadErr)
			return
		}
		if _, loadErr = external.CompareAndSave(ctx, doc.ConfigurationRevision, doc); loadErr != nil {
			t.Errorf("external CompareAndSave: %v", loadErr)
		}
	}
	result, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "reconcile-use-first", ExpectedRevision: &second.Revision},
		Model:     "ollama/reconcile-first",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != second.Revision+1 {
		t.Fatalf("UseModel() = %#v, %v", result, err)
	}
	canonical, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.ConfigurationRevision != result.Revision+1 {
		t.Fatalf("canonical revision = %d, want %d", canonical.ConfigurationRevision, result.Revision+1)
	}
	if got := stack.composition.lookup.DefaultID(); !strings.Contains(got, "reconcile-second") {
		t.Fatalf("live default after newer canonical commit = %q, want second writer model", got)
	}
}

func TestHostModelCommandCommitsForFutureActivationWithoutAssemblyRefresh(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	expected, err := stack.ControlStatus().ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.ConfigurationCommands().ConnectModel(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "host-model-refresh-warning", ExpectedRevision: &expected},
		Config: appserver.ConnectConfig{
			Provider: "ollama", Model: "refresh-model", BaseURL: "http://127.0.0.1:11434",
		},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != expected+1 {
		t.Fatalf("ConnectModel() = %#v, %v", result, err)
	}
	persisted, loadErr := stack.composition.authorities.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	candidate, candidateErr := newModelLookupFromDocument(persisted, stack.composition.lookup.contextWindow)
	if candidateErr != nil {
		t.Fatal(candidateErr)
	}
	if !candidate.HasAlias("ollama/refresh-model") || !stack.composition.lookup.HasAlias("ollama/refresh-model") {
		t.Fatalf("committed refresh model durable/live = %v/%v", candidate.HasAlias("ollama/refresh-model"), stack.composition.lookup.HasAlias("ollama/refresh-model"))
	}
}

func TestHostConnectModelSelectionsPreserveImageInput(t *testing.T) {
	enabled := true
	selections := hostConnectModelSelections(appserver.ConnectConfig{
		Model:      "acme-vision",
		ImageInput: &enabled,
	})
	if len(selections) != 1 || selections[0].ImageInput == nil || !*selections[0].ImageInput {
		t.Fatalf("model selections = %#v, want explicit image input", selections)
	}
}
