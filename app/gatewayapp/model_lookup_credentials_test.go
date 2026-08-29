package gatewayapp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestBrokenUnusedProviderCredentialDoesNotBlockObservationOrStartup(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	t.Cleanup(func() {
		if stack != nil {
			_ = stack.Close()
		}
	})
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaultID := stack.composition.lookup.DefaultID()
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "broken-unused-credential-connect",
			ExpectedRevision: &before.ConfigurationRevision,
		},
		Config: appserver.ConnectConfig{
			Provider: "openai",
			Model:    "broken-unused-credential-model",
			BaseURL:  "https://broken-unused-credential.example/v1",
			APIKey:   "broken-unused-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai/broken-unused-credential-model")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.apiKeyCredentials.Delete(ctx, configured.CredentialRef); err != nil {
		t.Fatal(err)
	}

	// A missing credential for a non-selected profile must not turn an
	// unrelated committed configuration change into a post-commit warning.
	if err := stack.useTestHostModel(ctx, session.SessionRef{}, defaultID); err != nil {
		t.Fatalf("UseModel(default) with broken unused credential: %v", err)
	}

	storeDir := stack.composition.authorities.storeDir
	workspace := stack.composition.workspace
	appName := stack.composition.authorities.appName
	userID := stack.composition.authorities.userID
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack = nil

	restarted, err := NewLocalStack(Config{
		AppName:      appName,
		UserID:       userID,
		StoreDir:     storeDir,
		WorkspaceKey: workspace.Key,
		WorkspaceCWD: workspace.CWD,
		ApprovalMode: "auto-review",
		Sandbox:      SandboxConfig{RequestedType: "host"},
		SkillDirs:    []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() with broken unused credential: %v", err)
	}
	stack = restarted
	if got := restarted.composition.lookup.DefaultID(); got != defaultID {
		t.Fatalf("restarted default model = %q, want %q", got, defaultID)
	}
	if _, err := restarted.composition.lookup.ResolveModel(ctx, configured.ID, 0); err == nil {
		t.Fatal("ResolveModel() with the broken target credential succeeded")
	}
}

func TestRuntimeCredentialSnapshotPinsLateModelAndProcessLocalChildCopy(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLookup, err := cloneSessionModelLookup(stack.composition.lookup, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeLookup.pinAPIKeyCredentials(ctx, providerProfileAPIKeyCredentialRefs(before)); err != nil {
		t.Fatal(err)
	}

	baseURL := "https://late-runtime-pin.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "late-runtime-pin-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "late-runtime-model", BaseURL: baseURL, APIKey: "late-runtime-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai/late-runtime-model")
	if err != nil {
		t.Fatal(err)
	}
	finishCredential, err := runtimeLookup.beginPinAPIKeyCredential(ctx, configured, stack.composition.lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer finishCredential(false)
	if _, err := runtimeLookup.upsert(configured, false); err != nil {
		t.Fatal(err)
	}
	finishCredential(true)
	childPin, err := runtimeLookup.materializeAPIKeyCredential(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	if childPin.Token != "late-runtime-secret" || childPin.PersistToken {
		t.Fatalf("process-local child pin = %#v", childPin)
	}

	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "late-runtime-pin-delete", ExpectedRevision: &connected.Revision},
		Model:     configured.ID,
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, err)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)
	if _, lookupErr := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); !errors.Is(lookupErr, os.ErrNotExist) {
		t.Fatalf("retired credential lookup error = %v, want os.ErrNotExist", lookupErr)
	}
	if _, err := runtimeLookup.ResolveModel(ctx, configured.ID, 0); err != nil {
		t.Fatalf("late-pinned Runtime model after credential retirement: %v", err)
	}

	childLookup, err := newModelLookupFromDocument(AppConfig{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childLookup.upsert(childPin, false); err != nil {
		t.Fatal(err)
	}
	if err := childLookup.pinAPIKeyCredentials(ctx, []string{childPin.CredentialRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := childLookup.ResolveModel(ctx, childPin.ID, 0); err != nil {
		t.Fatalf("child Runtime model after Host credential retirement: %v", err)
	}
}

func TestRuntimeCredentialSnapshotRetriesWhenHostRetiresAfterConfigRead(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://activation-retirement.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "activation-retirement-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "activation-retirement-model", BaseURL: baseURL, APIKey: "activation-retirement-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai/activation-retirement-model")
	if err != nil {
		t.Fatal(err)
	}

	stack.composition.lookup.mu.Lock()
	originalResolver := stack.composition.lookup.resolveAPIKey
	resolverStarted := make(chan struct{})
	continueResolver := make(chan struct{})
	defer func() {
		select {
		case <-continueResolver:
		default:
			close(continueResolver)
		}
	}()
	var started bool
	stack.composition.lookup.resolveAPIKey = func(ctx context.Context, ref string) (string, error) {
		if !started {
			started = true
			close(resolverStarted)
			<-continueResolver
		}
		return originalResolver(ctx, ref)
	}
	stack.composition.lookup.mu.Unlock()
	t.Cleanup(func() {
		stack.composition.lookup.mu.Lock()
		stack.composition.lookup.resolveAPIKey = originalResolver
		stack.composition.lookup.mu.Unlock()
	})

	assembler, ok := stack.sessionRuntimes.assembler.(*workspaceConfigAssembler)
	if !ok {
		t.Fatalf("Session Runtime assembler = %T", stack.sessionRuntimes.assembler)
	}
	type snapshotResult struct {
		doc    AppConfig
		lookup *modelLookup
		err    error
	}
	result := make(chan snapshotResult, 1)
	go func() {
		doc, lookup, loadErr := assembler.loadRuntimeModelSnapshot(ctx, active)
		result <- snapshotResult{doc: doc, lookup: lookup, err: loadErr}
	}()
	select {
	case <-resolverStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Runtime credential snapshot did not start")
	}

	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "activation-retirement-delete", ExpectedRevision: &connected.Revision},
		Model:     configured.ID,
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, err)
	}
	close(continueResolver)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.doc.ConfigurationRevision != deleted.Revision {
			t.Fatalf("Runtime snapshot revision = %d, want %d", got.doc.ConfigurationRevision, deleted.Revision)
		}
		if _, ok := got.lookup.Config(configured.ID); ok {
			t.Fatalf("Runtime snapshot retained deleted model %q", configured.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Runtime credential snapshot did not retry after Host retirement")
	}
}

func TestHostCredentialRetirementRecoveryRestoresPreCASDeletion(t *testing.T) {
	const (
		helperRootEnv     = "CAELIS_RETIREMENT_CRASH_HELPER_ROOT"
		helperRefEnv      = "CAELIS_RETIREMENT_CRASH_HELPER_REF"
		helperRevisionEnv = "CAELIS_RETIREMENT_CRASH_HELPER_REVISION"
	)
	if root := os.Getenv(helperRootEnv); root != "" {
		credentials, err := credentialstore.New(root)
		if err != nil {
			panic(err)
		}
		revision, err := strconv.ParseUint(os.Getenv(helperRevisionEnv), 10, 64)
		if err != nil {
			panic(err)
		}
		if _, err := credentials.BeginRetirement(context.Background(), []string{os.Getenv(helperRefEnv)}, revision); err != nil {
			panic(err)
		}
		// Deliberately bypass defers to reproduce abrupt process loss after the
		// receipt and deletion are durable but before any AppConfig CAS.
		os.Exit(0)
	}

	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://retirement-crash-recovery.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retirement-crash-recovery-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "retirement-crash-recovery-model", BaseURL: baseURL, APIKey: "retirement-crash-recovery-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)
	storeRoot := filepath.Dir(stack.composition.authorities.store.path)
	helper := exec.Command(os.Args[0], "-test.run=^TestHostCredentialRetirementRecoveryRestoresPreCASDeletion$")
	helper.Env = append(os.Environ(),
		helperRootEnv+"="+storeRoot,
		helperRefEnv+"="+ref,
		helperRevisionEnv+"="+strconv.FormatUint(connected.Revision, 10),
	)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("retirement crash helper: %v\n%s", err, output)
	}
	if _, err := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential after interrupted pre-CAS delete = %v, want os.ErrNotExist", err)
	}

	restartedCredentials, err := credentialstore.New(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverProviderCredentialRetirements(ctx, newAppConfigStore(storeRoot), restartedCredentials); err != nil {
		t.Fatal(err)
	}
	if source, err := restartedCredentials.LookupSource(ctx, ref); err != nil || source.APIKey != "retirement-crash-recovery-secret" {
		t.Fatalf("recovered credential = %#v, %v", source, err)
	}
}

func TestHostCredentialRetirementRecoveryPreservesPostCASDeletion(t *testing.T) {
	const (
		helperRootEnv      = "CAELIS_RETIREMENT_POST_CAS_HELPER_ROOT"
		helperRefEnv       = "CAELIS_RETIREMENT_POST_CAS_HELPER_REF"
		helperRevisionEnv  = "CAELIS_RETIREMENT_POST_CAS_HELPER_REVISION"
		helperProfileIDEnv = "CAELIS_RETIREMENT_POST_CAS_HELPER_PROFILE_ID"
	)
	if root := os.Getenv(helperRootEnv); root != "" {
		ctx := context.Background()
		credentials, err := credentialstore.New(root)
		if err != nil {
			panic(err)
		}
		revision, err := strconv.ParseUint(os.Getenv(helperRevisionEnv), 10, 64)
		if err != nil {
			panic(err)
		}
		if _, err := credentials.BeginRetirement(ctx, []string{os.Getenv(helperRefEnv)}, revision); err != nil {
			panic(err)
		}
		store := newAppConfigStore(root)
		doc, err := store.LoadContext(ctx)
		if err != nil {
			panic(err)
		}
		doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, os.Getenv(helperProfileIDEnv))
		if _, err := store.CompareAndSave(ctx, revision, doc); err != nil {
			panic(err)
		}
		// Reproduce abrupt loss after the AppConfig CAS but before receipt
		// removal and reference-lock release.
		os.Exit(0)
	}

	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "https://retirement-post-cas-recovery.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "retirement-post-cas-recovery-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "retirement-post-cas-recovery-model", BaseURL: baseURL, APIKey: "retirement-post-cas-recovery-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai/retirement-post-cas-recovery-model")
	if err != nil {
		t.Fatal(err)
	}
	ref := retainedAPIKeyReference("openai", "", baseURL)
	storeRoot := filepath.Dir(stack.composition.authorities.store.path)
	helper := exec.Command(os.Args[0], "-test.run=^TestHostCredentialRetirementRecoveryPreservesPostCASDeletion$")
	helper.Env = append(os.Environ(),
		helperRootEnv+"="+storeRoot,
		helperRefEnv+"="+ref,
		helperRevisionEnv+"="+strconv.FormatUint(connected.Revision, 10),
		helperProfileIDEnv+"="+modelprofile.BuildProviderID(configured.ID),
	)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("post-CAS retirement crash helper: %v\n%s", err, output)
	}

	restartedCredentials, err := credentialstore.New(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverProviderCredentialRetirements(ctx, newAppConfigStore(storeRoot), restartedCredentials); err != nil {
		t.Fatal(err)
	}
	if _, err := restartedCredentials.LookupSource(ctx, ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential after post-CAS recovery = %v, want os.ErrNotExist", err)
	}
	callbackCalled := false
	if err := restartedCredentials.RecoverRetirements(ctx, func(context.Context) (map[string]struct{}, error) {
		callbackCalled = true
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbackCalled {
		t.Fatal("post-CAS recovery left a retirement receipt for a second sweep")
	}
}

func TestRuntimeCredentialSnapshotIgnoresOrphanModelWithRetiredCredential(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}

	validURL := "https://reachable-runtime-model.example/v1"
	valid, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-runtime-valid-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "reachable-runtime-model", BaseURL: validURL, APIKey: "reachable-runtime-secret",
		},
	})
	if err != nil || valid.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(valid) = %#v, %v", valid, err)
	}
	orphanURL := "https://orphan-runtime-model.example/v1"
	orphaned, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-runtime-connect", ExpectedRevision: &valid.Revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "retired-reachable-model,stable-orphan-model", BaseURL: orphanURL, APIKey: "orphan-runtime-secret",
		},
	})
	if err != nil || orphaned.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(orphan endpoint) = %#v, %v", orphaned, err)
	}
	orphanConfig, err := stack.composition.lookup.ResolveConfig("openai/stable-orphan-model")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, modelprofile.BuildProviderID(orphanConfig.ID))
	saved, err := stack.composition.authorities.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "orphan-runtime-delete-final-profile", ExpectedRevision: &saved.ConfigurationRevision},
		Model:     "openai/retired-reachable-model",
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel(final reachable orphan endpoint profile) = %#v, %v", deleted, err)
	}
	orphanRef := retainedAPIKeyReference("openai", "", orphanURL)
	if _, err := stack.composition.authorities.apiKeyCredentials.LookupSource(ctx, orphanRef); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan endpoint credential error = %v, want os.ErrNotExist", err)
	}

	assembler, ok := stack.sessionRuntimes.assembler.(*workspaceConfigAssembler)
	if !ok {
		t.Fatalf("Session Runtime assembler = %T", stack.sessionRuntimes.assembler)
	}
	snapshotDoc, lookup, err := assembler.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		t.Fatalf("loadRuntimeModelSnapshot() with orphan model: %v", err)
	}
	if snapshotDoc.ConfigurationRevision != deleted.Revision {
		t.Fatalf("Runtime snapshot revision = %d, want %d", snapshotDoc.ConfigurationRevision, deleted.Revision)
	}
	if _, err := lookup.ResolveModel(ctx, "openai/reachable-runtime-model", 0); err != nil {
		t.Fatalf("ResolveModel(valid reachable model): %v", err)
	}
	if _, err := lookup.ResolveModel(ctx, orphanConfig.ID, 0); err == nil {
		t.Fatal("orphan model implicitly retained its retired credential in Runtime snapshot")
	}
}

func TestRuntimeCredentialSnapshotRollsBackUncommittedLatePin(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLookup, err := cloneSessionModelLookup(stack.composition.lookup, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeLookup.pinAPIKeyCredentials(ctx, providerProfileAPIKeyCredentialRefs(before)); err != nil {
		t.Fatal(err)
	}

	baseURL := "https://rolled-back-runtime-pin.example/v1"
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "rolled-back-runtime-pin-connect", ExpectedRevision: &before.ConfigurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "rolled-back-runtime-model", BaseURL: baseURL, APIKey: "rolled-back-runtime-secret",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai/rolled-back-runtime-model")
	if err != nil {
		t.Fatal(err)
	}
	finishCredential, err := runtimeLookup.beginPinAPIKeyCredential(ctx, configured, stack.composition.lookup)
	if err != nil {
		t.Fatal(err)
	}
	finishCredential(false)

	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "rolled-back-runtime-pin-delete", ExpectedRevision: &connected.Revision},
		Model:     configured.ID,
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, err)
	}
	if _, err := runtimeLookup.materializeAPIKeyCredential(ctx, configured); err == nil {
		t.Fatal("rolled-back Runtime pin retained credential after Host retirement")
	}
}
