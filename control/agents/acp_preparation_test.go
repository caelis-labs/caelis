package agents

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeACPPrepareRequestHasNoHiddenConnectFields(t *testing.T) {
	t.Parallel()

	request := NormalizeACPPrepareRequest(ACPPrepareRequest{
		AdapterID: " Codex ", Launcher: " NPX ", CommandLine: "  codex-acp  ",
		ModelID: " model-1 ", CWD: " /workspace ", ParentRef: " parent ",
	})
	want := ACPPrepareRequest{
		AdapterID: "codex", Launcher: LauncherChoiceNPX, CommandLine: "codex-acp",
		ModelID: "model-1", CWD: "/workspace", ParentRef: "parent",
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("NormalizeACPPrepareRequest() = %#v, want %#v", request, want)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"config_values", "discovery", "authentication"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("ACPPrepareRequest wire contains %q: %s", forbidden, payload)
		}
	}
}

func TestACPPreparationStatesAndWireOwnership(t *testing.T) {
	t.Parallel()

	planned := sealACPPreparationForTest(t, ACPPreparation{
		Ref: acpPreparationRefForTest(1), State: PreparationStatePlanned,
		PrincipalID: "principal-private", OperationID: "operation-private",
		IntentDigest: strings.Repeat("a", 64), ParentRef: acpPreparationRefForTest(2),
		Request: ACPPrepareRequest{
			AdapterID: "codex", Launcher: LauncherChoiceNPX, ParentRef: acpPreparationRefForTest(2),
		},
		ObservedRevision: 7,
		CreatedAt:        time.Date(2026, 8, 11, 2, 0, 0, 0, time.FixedZone("offset", 8*60*60)),
		ExpiresAt:        time.Date(2026, 8, 11, 3, 0, 0, 0, time.FixedZone("offset", 8*60*60)),
	})
	if planned.CreatedAt.Location() != time.UTC || planned.ExpiresAt.Location() != time.UTC {
		t.Fatalf("preparation times were not normalized: %#v", planned)
	}
	payload, err := json.Marshal(planned)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"principal-private", "operation-private", strings.Repeat("a", 64)} {
		if strings.Contains(string(payload), hidden) {
			t.Fatalf("ACPPreparation wire exposed trusted ownership %q: %s", hidden, payload)
		}
	}
	if strings.Contains(string(payload), `"connection"`) {
		t.Fatalf("planned ACPPreparation wire exposed an unresolved connection: %s", payload)
	}

	needsAuth := planned
	needsAuth.State = PreparationStateNeedsAuth
	needsAuth.Connection = Connection{ID: "codex", Launcher: Launcher{Kind: LaunchKindPackageExec, Command: "npx"}}
	needsAuth.AuthenticationMethods = []AuthenticationChallengeMethod{{
		ID: " terminal ", Name: " Login ", Type: AuthenticationTerminal,
	}}
	needsAuth = sealACPPreparationForTest(t, needsAuth)
	challenge, err := needsAuth.AuthenticationChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if challenge.PreparationRef != needsAuth.Ref || challenge.ContentDigest != needsAuth.ContentDigest ||
		len(challenge.Methods) != 1 || challenge.Methods[0].ID != "terminal" {
		t.Fatalf("AuthenticationChallenge() = %#v", challenge)
	}

	ready := needsAuth
	ready.State = PreparationStateReady
	ready.SelectedAuthentication = Authentication{MethodID: "terminal", Type: AuthenticationTerminal}
	ready.Connection.Authentication = ready.SelectedAuthentication
	ready.Discovery = DiscoverySnapshot{
		ConnectionID: ready.Connection.ID, LaunchFingerprint: LaunchFingerprint(ready.Connection.Launcher),
		Authentication: ready.SelectedAuthentication, DiscoveredAt: ready.CreatedAt.Add(time.Minute),
	}
	ready = sealACPPreparationForTest(t, ready)
	if err := ValidateACPPreparation(ready); err != nil {
		t.Fatal(err)
	}

	tampered := ready
	tampered.CleanupWarning = "changed"
	if err := ValidateACPPreparation(tampered); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("ValidateACPPreparation(tampered) error = %v", err)
	}
}

func TestValidateACPPreparationRejectsInvalidStateContent(t *testing.T) {
	t.Parallel()

	base := ACPPreparation{
		Ref: acpPreparationRefForTest(3), State: PreparationStatePlanned,
		PrincipalID: "principal", OperationID: "operation", IntentDigest: strings.Repeat("b", 64),
		Request:   ACPPrepareRequest{AdapterID: "agent", Launcher: LauncherChoiceGlobal},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	base.AuthenticationMethods = []AuthenticationChallengeMethod{{ID: "login", Type: AuthenticationAgent}}
	if _, err := SealACPPreparation(base); err == nil || !strings.Contains(err.Error(), "planned") {
		t.Fatalf("SealACPPreparation(planned challenge) error = %v", err)
	}

	base.State = PreparationStateNeedsAuth
	base.Connection = Connection{ID: "agent", Launcher: Launcher{Command: "agent-acp"}}
	base.AuthenticationMethods = nil
	if _, err := SealACPPreparation(base); err == nil || !strings.Contains(err.Error(), "needs_auth") {
		t.Fatalf("SealACPPreparation(needs_auth without methods) error = %v", err)
	}
}

func TestValidateACPPreparationRequiresRequestBeforeResolvedConnection(t *testing.T) {
	t.Parallel()

	base := ACPPreparation{
		Ref: acpPreparationRefForTest(4), State: PreparationStatePlanned,
		PrincipalID: "principal", OperationID: "operation", IntentDigest: strings.Repeat("c", 64),
		Request:   ACPPrepareRequest{AdapterID: "custom", Launcher: LauncherChoiceCommand, CommandLine: "custom-acp --stdio"},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	planned := sealACPPreparationForTest(t, base)
	if planned.Connection.ID != "" || planned.Connection.Launcher.Command != "" {
		t.Fatalf("planned preparation unexpectedly resolved a connection: %#v", planned.Connection)
	}

	withConnection := base
	withConnection.Connection = Connection{ID: "custom", Launcher: Launcher{Command: "custom-acp", Args: []string{"--stdio"}}}
	if _, err := SealACPPreparation(withConnection); err != nil {
		t.Fatalf("SealACPPreparation(planned with resolved connection) error = %v", err)
	}

	needsAuth := planned
	needsAuth.State = PreparationStateNeedsAuth
	needsAuth.AuthenticationMethods = []AuthenticationChallengeMethod{{ID: "login", Type: AuthenticationAgent}}
	if _, err := SealACPPreparation(needsAuth); err == nil || !strings.Contains(err.Error(), "connection") {
		t.Fatalf("SealACPPreparation(needs_auth without connection) error = %v", err)
	}

	tamperedRequest := planned
	tamperedRequest.Request.ModelID = "other"
	if err := ValidateACPPreparation(tamperedRequest); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("ValidateACPPreparation(tampered request) error = %v", err)
	}
}

func sealACPPreparationForTest(t *testing.T, preparation ACPPreparation) ACPPreparation {
	t.Helper()
	preparation.ContentDigest = ""
	sealed, err := SealACPPreparation(preparation)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func acpPreparationRefForTest(fill byte) string {
	return "acpp_" + base64.RawURLEncoding.EncodeToString(bytesOf(fill, 32))
}

func bytesOf(fill byte, count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = fill
	}
	return out
}
