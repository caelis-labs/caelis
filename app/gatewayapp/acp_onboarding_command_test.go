package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	acpclient "github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestACPPrepareCommandRecoversIntentOnlyReceiptWithoutRepeatingProcess(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	marker := filepath.Join(t.TempDir(), "starts")
	expected := currentConfigurationRevision(t, stack)
	request := appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &expected},
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: gatewayACPOnboardingHelperCommand("ready", marker),
			ModelID:     controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
	}
	failing := &completeFailingOperationStore{OperationStore: stack.operations}
	firstCommands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{}, Operations: failing, Backend: stack,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := firstCommands.PrepareACP(context.Background(), principal, request)
	if firstErr == nil || first.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("PrepareACP(first) = %#v, %v; want intent-only unknown after completion fault", first, firstErr)
	}
	if starts := onboardingHelperStarts(t, marker); starts != 1 {
		t.Fatalf("helper starts after first prepare = %d, want 1", starts)
	}

	restartedOperations := appserver.NewFileOperationStore(filepath.Join(stack.composition.storeDir, "control-operations"))
	if err := restartedOperations.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	restartedPreparations, err := newACPPreparationStore(stack.composition.storeDir)
	if err != nil {
		t.Fatal(err)
	}
	stack.acpPreparations = restartedPreparations
	restartedCommands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{}, Operations: restartedOperations, Backend: stack,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, recoveryErr := restartedCommands.PrepareACP(context.Background(), principal, request)
	if recoveryErr != nil || recovered.Outcome != appserver.OutcomeCommitted || recovered.Resource == nil ||
		recovered.Resource.Kind != appserver.CommandResourceACPPreparation {
		t.Fatalf("PrepareACP(recovered) = %#v, %v", recovered, recoveryErr)
	}
	if starts := onboardingHelperStarts(t, marker); starts != 1 {
		t.Fatalf("helper starts after observational recovery = %d, want 1", starts)
	}
	prepared, getErr := stack.acpPreparations.Get(context.Background(), recovered.Resource.Ref)
	if getErr != nil || prepared.State != controlagents.PreparationStateReady || prepared.ContentDigest != recovered.Resource.Digest {
		t.Fatalf("recovered preparation = %#v, %v", prepared, getErr)
	}
}

func TestStackACPCommandRecoveryCapabilityIsExplicit(t *testing.T) {
	stack := &Stack{}
	for _, test := range []struct {
		action appserver.Action
		want   bool
	}{
		{action: appserver.ActionACPAgentPrepare, want: true},
		{action: appserver.ActionACPAgentPrepareAuth, want: true},
		{action: appserver.ActionACPAgentConnect, want: false},
		{action: appserver.ActionPrompt, want: false},
		{action: appserver.Action("unknown.action"), want: false},
	} {
		if got := stack.CanRecoverControlCommand(test.action); got != test.want {
			t.Fatalf("CanRecoverControlCommand(%q) = %v, want %v", test.action, got, test.want)
		}
	}
}

func TestACPPrepareConcurrentRetryPreservesPostCommitWarningReceipt(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	marker := filepath.Join(t.TempDir(), "starts")
	expected := currentConfigurationRevision(t, stack)
	request := appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &expected},
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: gatewayACPOnboardingHelperCommand("ready", marker),
			ModelID:     controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
	}
	backend := &warningACPCommandBackend{Stack: stack}
	blockingOperations := &blockingCompleteOperationStore{
		OperationStore: stack.operations,
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	creator, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{}, Operations: blockingOperations, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	retryOperations := appserver.NewFileOperationStore(filepath.Join(stack.composition.storeDir, "control-operations"))
	if err := retryOperations.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	retry, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{}, Operations: retryOperations, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	type response struct {
		result appserver.CommandResult
		err    error
	}
	creatorDone := make(chan response, 1)
	go func() {
		result, err := creator.PrepareACP(context.Background(), principal, request)
		creatorDone <- response{result: result, err: err}
	}()
	select {
	case <-blockingOperations.started:
	case <-time.After(5 * time.Second):
		t.Fatal("creator did not reach operation completion")
	}

	retryDone := make(chan response, 1)
	go func() {
		result, err := retry.PrepareACP(context.Background(), principal, request)
		retryDone <- response{result: result, err: err}
	}()
	select {
	case got := <-retryDone:
		t.Fatalf("retry returned before warning receipt completed: %#v, %v", got.result, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(blockingOperations.release)
	created := <-creatorDone
	replayed := <-retryDone
	if created.err != nil || replayed.err != nil || !equalCommandResult(created.result, replayed.result) {
		t.Fatalf("creator/retry = %#v, %v / %#v, %v", created.result, created.err, replayed.result, replayed.err)
	}
	if created.result.Detail != warningACPCommandDetail {
		t.Fatalf("warning detail = %q, want %q", created.result.Detail, warningACPCommandDetail)
	}
	if got := backend.recoverCalls.Load(); got != 0 {
		t.Fatalf("recovery calls while creator completed = %d, want 0", got)
	}

	restartedOperations := appserver.NewFileOperationStore(filepath.Join(stack.composition.storeDir, "control-operations"))
	if err := restartedOperations.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{}, Operations: restartedOperations, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, restartErr := restarted.PrepareACP(context.Background(), principal, request)
	if restartErr != nil || !equalCommandResult(afterRestart, created.result) {
		t.Fatalf("PrepareACP(after restart) = %#v, %v; want %#v", afterRestart, restartErr, created.result)
	}
	if starts := onboardingHelperStarts(t, marker); starts != 1 {
		t.Fatalf("helper starts = %d, want one despite retry and restart", starts)
	}
}

func TestACPPrepareAndAuthenticationCommandsPersistExplicitChallenge(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	marker := filepath.Join(t.TempDir(), "starts")
	expected := currentConfigurationRevision(t, stack)
	preparedReceipt, err := stack.AgentCommands().PrepareACP(context.Background(), principal, appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &expected},
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: gatewayACPOnboardingHelperCommand("needs-auth", marker),
			ModelID:     controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
	})
	if err != nil || preparedReceipt.Outcome != appserver.OutcomeCommitted || preparedReceipt.Resource == nil {
		t.Fatalf("PrepareACP() = %#v, %v", preparedReceipt, err)
	}
	challenge, err := stack.acpPreparations.Get(context.Background(), preparedReceipt.Resource.Ref)
	if err != nil || challenge.State != controlagents.PreparationStateNeedsAuth || len(challenge.AuthenticationMethods) != 1 ||
		challenge.AuthenticationMethods[0].ID != "agent-login" {
		t.Fatalf("challenge preparation = %#v, %v", challenge, err)
	}
	current := currentConfigurationRevision(t, stack)
	authReceipt, err := stack.AgentCommands().PrepareACPAuthentication(context.Background(), principal, appserver.PrepareACPAuthenticationRequest{
		WriteBase:      appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &current},
		PreparationRef: challenge.Ref, PreparationDigest: challenge.ContentDigest, MethodID: "agent-login",
	})
	if err != nil || authReceipt.Outcome != appserver.OutcomeCommitted || authReceipt.Resource == nil {
		t.Fatalf("PrepareACPAuthentication() = %#v, %v", authReceipt, err)
	}
	ready, err := stack.acpPreparations.Get(context.Background(), authReceipt.Resource.Ref)
	if err != nil || ready.State != controlagents.PreparationStateReady || ready.ParentRef != challenge.Ref ||
		ready.SelectedAuthentication.MethodID != "agent-login" {
		t.Fatalf("authenticated preparation = %#v, %v", ready, err)
	}
	if starts := onboardingHelperStarts(t, marker); starts != 2 {
		t.Fatalf("helper starts = %d, want one probe plus one explicit authentication", starts)
	}
}

func TestACPPrepareCancellationIsUnknownAndNeverRepeatsEffect(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	marker := filepath.Join(t.TempDir(), "starts")
	expected := currentConfigurationRevision(t, stack)
	request := appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &expected},
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: gatewayACPOnboardingHelperCommand("block", marker),
			ModelID:     controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	type response struct {
		result appserver.CommandResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := stack.AgentCommands().PrepareACP(ctx, principal, request)
		done <- response{result: result, err: err}
	}()
	waitForOnboardingHelperStart(t, marker)
	cancel()
	var first response
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PrepareACP() did not return after cancellation")
	}
	if first.err == nil || first.result.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("PrepareACP(canceled) = %#v, %v; want unknown", first.result, first.err)
	}
	replayed, replayErr := stack.AgentCommands().PrepareACP(context.Background(), principal, request)
	if replayErr != nil || replayed.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("PrepareACP(replay canceled) = %#v, %v", replayed, replayErr)
	}
	if starts := onboardingHelperStarts(t, marker); starts != 1 {
		t.Fatalf("helper starts after replay = %d, want 1", starts)
	}
}

func TestConnectPreparedACPCommitsExactPreparationAndReplaysReceipt(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	prepared := seedReadyACPPreparation(t, stack, principal.ID, "prepare-connect")
	expected := currentConfigurationRevision(t, stack)
	request := appserver.ConnectACPRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      uuid.NewString(),
			ExpectedRevision: &expected,
		},
		PreparationRef:    prepared.Ref,
		PreparationDigest: prepared.ContentDigest,
	}

	receipt, err := stack.AgentCommands().ConnectACP(context.Background(), principal, request)
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted || receipt.Revision != expected+1 {
		t.Fatalf("ConnectACP() = %#v, %v", receipt, err)
	}
	if receipt.Resource == nil || receipt.Resource.Kind != appserver.CommandResourceModelProfile || receipt.Resource.Ref == "" || receipt.Resource.Digest != prepared.ContentDigest {
		t.Fatalf("ConnectACP() resource = %#v", receipt.Resource)
	}
	doc, err := stack.composition.store.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if doc.ConfigurationRevision != receipt.Revision {
		t.Fatalf("configuration revision = %d, want %d", doc.ConfigurationRevision, receipt.Revision)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, receipt.Resource.Ref); !ok {
		t.Fatalf("persisted profiles = %#v, want %q", doc.ModelProfiles, receipt.Resource.Ref)
	}
	if _, ok := controlagents.LookupConnection(doc.ExternalAgents, prepared.Connection.ID); !ok {
		t.Fatalf("persisted external Agents = %#v, want connection %q", doc.ExternalAgents, prepared.Connection.ID)
	}

	replayed, replayErr := stack.AgentCommands().ConnectACP(context.Background(), principal, request)
	if replayErr != nil || !equalCommandResult(replayed, receipt) {
		t.Fatalf("ConnectACP(replay) = %#v, %v; want %#v", replayed, replayErr, receipt)
	}
	afterReplay := currentConfigurationRevision(t, stack)
	if afterReplay != receipt.Revision {
		t.Fatalf("replay configuration revision = %d, want %d", afterReplay, receipt.Revision)
	}
}

func TestConnectPreparedACPRejectsForeignOrChangedPreparation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	prepared := seedReadyACPPreparation(t, stack, stack.composition.userID, "prepare-owned")
	expected := currentConfigurationRevision(t, stack)
	tests := []struct {
		name      string
		principal appserver.Principal
		digest    string
	}{
		{name: "foreign principal", principal: appserver.Principal{ID: "someone-else"}, digest: prepared.ContentDigest},
		{name: "changed digest", principal: appserver.Principal{ID: stack.composition.userID}, digest: strings.Repeat("f", 64)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := appserver.ConnectACPRequest{
				WriteBase:      appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &expected},
				PreparationRef: prepared.Ref, PreparationDigest: tc.digest,
			}
			receipt, err := stack.AgentCommands().ConnectACP(context.Background(), tc.principal, request)
			if err == nil || receipt.Outcome != appserver.OutcomeRejected {
				t.Fatalf("ConnectACP() = %#v, %v; want rejected", receipt, err)
			}
		})
	}
	if got := currentConfigurationRevision(t, stack); got != expected {
		t.Fatalf("rejected connect changed revision to %d, want %d", got, expected)
	}
}

func TestConnectPreparedACPCASCommitsForFutureActivation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	prepared := seedReadyACPPreparation(t, stack, principal.ID, "prepare-warning")
	expected := currentConfigurationRevision(t, stack)
	stale := expected - 1
	conflictRequest := appserver.ConnectACPRequest{
		WriteBase:      appserver.WriteBase{OperationID: uuid.NewString(), ExpectedRevision: &stale},
		PreparationRef: prepared.Ref, PreparationDigest: prepared.ContentDigest,
	}
	conflicted, err := stack.AgentCommands().ConnectACP(context.Background(), principal, conflictRequest)
	if err == nil || conflicted.Outcome != appserver.OutcomeConflicted || conflicted.Revision != expected {
		t.Fatalf("ConnectACP(stale) = %#v, %v", conflicted, err)
	}

	request := conflictRequest
	request.OperationID = uuid.NewString()
	request.ExpectedRevision = &expected
	receipt, err := stack.AgentCommands().ConnectACP(context.Background(), principal, request)
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectACP() = %#v, %v", receipt, err)
	}
	doc, loadErr := stack.composition.store.LoadContext(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if doc.ConfigurationRevision != expected+1 {
		t.Fatalf("committed config revision = %d, want %d", doc.ConfigurationRevision, expected+1)
	}
	if _, ok := controlagents.LookupConnection(doc.ExternalAgents, prepared.Connection.ID); !ok {
		t.Fatalf("committed connection is missing: %#v", doc.ExternalAgents)
	}
}

func TestPrepareACPAuthenticationRejectsUnavailableTerminalCapabilityBeforeEffect(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	principal := appserver.Principal{ID: stack.composition.userID}
	parent := seedNeedsAuthACPPreparation(t, stack, principal.ID, "prepare-terminal")
	expected := currentConfigurationRevision(t, stack)
	receipt, err := stack.AgentCommands().PrepareACPAuthentication(context.Background(), principal, appserver.PrepareACPAuthenticationRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      uuid.NewString(),
			ExpectedRevision: &expected,
		},
		PreparationRef: parent.Ref, PreparationDigest: parent.ContentDigest, MethodID: "terminal-login",
	})
	if err == nil || receipt.Outcome != appserver.OutcomeRejected || receipt.ErrorCode != "failed_precondition" {
		t.Fatalf("PrepareACPAuthentication() = %#v, %v; want rejected failed_precondition", receipt, err)
	}
	if got := currentConfigurationRevision(t, stack); got != expected {
		t.Fatalf("rejected terminal auth changed revision to %d, want %d", got, expected)
	}
	observed, getErr := stack.acpPreparations.Get(context.Background(), parent.Ref)
	if getErr != nil || observed.State != controlagents.PreparationStateNeedsAuth || observed.ContentDigest != parent.ContentDigest {
		t.Fatalf("parent preparation after rejection = %#v, %v", observed, getErr)
	}
}

func seedNeedsAuthACPPreparation(t *testing.T, stack *Stack, principalID, operationID string) controlagents.ACPPreparation {
	t.Helper()
	command := writeExternalAgentExecutable(t, t.TempDir(), "terminal-auth-acp")
	planned, err := stack.acpPreparations.CreatePlanned(context.Background(), controlagents.ACPPreparation{
		State:        controlagents.PreparationStatePlanned,
		PrincipalID:  principalID,
		OperationID:  operationID,
		IntentDigest: strings.Repeat("b", 64),
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: command, ModelID: controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
		ObservedRevision: currentConfigurationRevision(t, stack),
	})
	if err != nil {
		t.Fatal(err)
	}
	needsAuth := planned
	needsAuth.State = controlagents.PreparationStateNeedsAuth
	needsAuth.Connection = controlagents.Connection{
		ID: "terminal-auth-acp", Name: "Terminal Auth ACP",
		Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: command, WorkDir: stack.composition.workspace.CWD},
	}
	needsAuth.AuthenticationMethods = []controlagents.AuthenticationChallengeMethod{{
		ID: "terminal-login", Name: "Terminal login", Type: controlagents.AuthenticationTerminal,
	}}
	needsAuth, err = stack.acpPreparations.Save(context.Background(), planned.ContentDigest, needsAuth)
	if err != nil {
		t.Fatal(err)
	}
	return needsAuth
}

func seedReadyACPPreparation(t *testing.T, stack *Stack, principalID, operationID string) controlagents.ACPPreparation {
	t.Helper()
	command := writeExternalAgentExecutable(t, t.TempDir(), "prepared-acp")
	planned, err := stack.acpPreparations.CreatePlanned(context.Background(), controlagents.ACPPreparation{
		State:        controlagents.PreparationStatePlanned,
		PrincipalID:  principalID,
		OperationID:  operationID,
		IntentDigest: strings.Repeat("a", 64),
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: command, ModelID: controlagents.DefaultRemoteModelID, CWD: stack.composition.workspace.CWD,
		},
		ObservedRevision: currentConfigurationRevision(t, stack),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := planned
	ready.State = controlagents.PreparationStateReady
	ready.Connection = controlagents.Connection{
		ID: "prepared-acp", Name: "Prepared ACP",
		Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: command, WorkDir: stack.composition.workspace.CWD},
	}
	ready.Discovery = controlagents.DiscoverySnapshot{
		ConnectionID: ready.Connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(ready.Connection.Launcher),
		CWD: stack.composition.workspace.CWD, SelectedModelID: controlagents.DefaultRemoteModelID, DiscoveredAt: time.Now().UTC(),
	}
	ready, err = stack.acpPreparations.Save(context.Background(), planned.ContentDigest, ready)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

type completeFailingOperationStore struct {
	appserver.OperationStore
}

func (s *completeFailingOperationStore) Complete(
	context.Context,
	appserver.OperationIntent,
	appserver.CommandResult,
) (appserver.OperationRecord, error) {
	return appserver.OperationRecord{}, errors.New("test operation completion fault")
}

const warningACPCommandDetail = "ACP preparation committed with a warning: test post-commit durability warning"

type warningACPCommandBackend struct {
	*Stack
	recoverCalls atomic.Int32
}

func (b *warningACPCommandBackend) ExecuteControlCommand(
	ctx context.Context,
	principal appserver.Principal,
	action appserver.Action,
	request any,
) (appserver.CommandResult, error) {
	result, err := b.Stack.ExecuteControlCommand(ctx, principal, action, request)
	if err == nil && action == appserver.ActionACPAgentPrepare && result.Outcome == appserver.OutcomeCommitted {
		result.Detail = warningACPCommandDetail
	}
	return result, err
}

func (b *warningACPCommandBackend) RecoverControlCommand(
	ctx context.Context,
	principal appserver.Principal,
	intent appserver.OperationIntent,
	request any,
) (appserver.CommandResult, bool, error) {
	b.recoverCalls.Add(1)
	return b.Stack.RecoverControlCommand(ctx, principal, intent, request)
}

type blockingCompleteOperationStore struct {
	appserver.OperationStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingCompleteOperationStore) Complete(
	ctx context.Context,
	intent appserver.OperationIntent,
	result appserver.CommandResult,
) (appserver.OperationRecord, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.OperationStore.Complete(ctx, intent, result)
}

func gatewayACPOnboardingHelperCommand(mode, marker string) string {
	return strings.Join([]string{
		strconv.Quote(os.Args[0]),
		"-test.run=^TestGatewayACPOnboardingHelperProcess$",
		"--",
		"caelis-onboarding-helper",
		strconv.Quote(strings.TrimSpace(mode)),
		strconv.Quote(strings.TrimSpace(marker)),
	}, " ")
}

func onboardingHelperStarts(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), "started\n")
}

func waitForOnboardingHelperStart(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if onboardingHelperStarts(t, marker) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ACP onboarding helper did not start")
}

func TestGatewayACPOnboardingHelperProcess(t *testing.T) {
	args := os.Args
	markerIndex := -1
	for index, arg := range args {
		if arg == "caelis-onboarding-helper" {
			markerIndex = index
			break
		}
	}
	if markerIndex < 0 {
		return
	}
	if markerIndex+2 >= len(args) {
		t.Fatal("missing ACP onboarding helper mode or marker")
	}
	mode := args[markerIndex+1]
	marker := args[markerIndex+2]
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("started\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	authenticated := false
	connection := jsonrpc.New(os.Stdin, os.Stdout)
	err = connection.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case acpclient.MethodInitialize:
			if mode == "block" {
				select {}
			}
			response := acpclient.InitializeResponse{
				ProtocolVersion: 1,
				AgentCapabilities: schema.AgentCapabilities{
					SessionCapabilities: map[string]json.RawMessage{"close": json.RawMessage(`{}`)},
				},
			}
			if mode == "needs-auth" {
				response.AuthMethods = []json.RawMessage{json.RawMessage(`{"id":"agent-login","name":"Agent login"}`)}
			}
			return response, nil
		case acpclient.MethodAuthenticate:
			var request acpclient.AuthenticateRequest
			if err := json.Unmarshal(message.Params, &request); err != nil || request.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authenticate request"}
			}
			authenticated = true
			return acpclient.AuthenticateResponse{}, nil
		case acpclient.MethodSessionNew:
			if mode == "needs-auth" && !authenticated {
				return nil, &jsonrpc.RPCError{Code: acpclient.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			return acpclient.NewSessionResponse{
				SessionID: "onboarding-session",
				ConfigOptions: []acpclient.SessionConfigOption{{
					ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "remote-model",
					Options: []acpclient.SessionConfigSelectOption{{Value: "remote-model", Name: "Remote Model"}},
				}},
			}, nil
		case acpclient.MethodSessionClose:
			return acpclient.CloseSessionResponse{}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: fmt.Sprintf("unknown method %s", message.Method)}
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func currentConfigurationRevision(t *testing.T, stack *Stack) uint64 {
	t.Helper()
	revision, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func equalCommandResult(left, right appserver.CommandResult) bool {
	if left.Resource == nil || right.Resource == nil {
		return left == right
	}
	leftResource, rightResource := *left.Resource, *right.Resource
	left.Resource, right.Resource = nil, nil
	return left == right && leftResource == rightResource
}
