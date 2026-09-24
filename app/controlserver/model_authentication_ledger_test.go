package controlserver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

type modelAuthBackend struct {
	effects atomic.Int32
	run     func(context.Context) (appserver.CommandResult, error)
}

func (b *modelAuthBackend) ExecuteControlCommand(ctx context.Context, _ appserver.Principal, _ appserver.Action, _ any) (appserver.CommandResult, error) {
	b.effects.Add(1)
	return b.run(ctx)
}

func newModelAuthLedgerServer(t *testing.T, backend *modelAuthBackend) *httptest.Server {
	t.Helper()
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{Authorizer: appserver.ProductCommandAuthorizer{}, Operations: appserver.NewMemoryOperationStore(), Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Configuration = commands
	server, err := New(HandlerConfig{Services: services, Authenticator: AuthenticatorFunc(func(r *http.Request) (appserver.Principal, error) {
		p := appserver.Principal{ID: r.Header.Get("Authorization")}
		if p.ID == "application" {
			p.ID, p.ApplicationID, p.ConnectionID = "owner", "bot", "connection"
		}
		return p, nil
	}), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	return host
}

func openModelAuthStream(t *testing.T, ctx context.Context, host *httptest.Server, principal, model string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":"connect","expected_revision":"1","config":{"provider":"xai","model":%q}}`, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host.URL+apiPrefix+"/configuration/connect-model", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", principal)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Idempotency-Key", "connect")
	req.Header.Set("If-Match", `"1"`)
	response, err := host.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func readModelAuthSnapshot(t *testing.T, scanner *bufio.Scanner, match func(appserver.ModelAuthenticationSnapshot) bool) appserver.ModelAuthenticationSnapshot {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var snapshot appserver.ModelAuthenticationSnapshot
		if err := wirev1.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snapshot); err != nil {
			t.Fatal(err)
		}
		if match(snapshot) {
			return snapshot
		}
	}
	t.Fatalf("authentication stream ended before expected snapshot: %v", scanner.Err())
	return appserver.ModelAuthenticationSnapshot{}
}

func TestModelAuthenticationDisconnectPreservesLedgerOutcomeWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan struct{})
	backend := &modelAuthBackend{run: func(ctx context.Context) (appserver.CommandResult, error) {
		defer close(finished)
		_, err := modelconfig.RequestAuthInput(ctx, modelconfig.AuthInputRequest{Prompt: "Callback URL", Secret: true})
		return appserver.CommandResult{}, err
	}}
	host := newModelAuthLedgerServer(t, backend)
	requestContext, stopRequest := context.WithCancel(ctx)
	first := openModelAuthStream(t, requestContext, host, "owner", "grok")
	scanner := bufio.NewScanner(first.Body)
	challenge := readModelAuthSnapshot(t, scanner, func(s appserver.ModelAuthenticationSnapshot) bool { return s.ChallengeID != "" })
	competing := openModelAuthStream(t, ctx, host, "owner", "grok")
	defer competing.Body.Close()
	if competing.StatusCode != http.StatusConflict || backend.effects.Load() != 1 {
		t.Fatalf("competing stream status = %d, native dispatches = %d", competing.StatusCode, backend.effects.Load())
	}
	stopRequest()
	_ = first.Body.Close()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel provider authentication")
	}
	// A retry may still meet the first handler's registry cleanup. A conflict
	// does not authorize a new operation identity or a second native dispatch.
	var retry *http.Response
	for {
		retry = openModelAuthStream(t, ctx, host, "owner", "grok")
		if retry.StatusCode == http.StatusOK {
			break
		}
		if retry.StatusCode != http.StatusConflict {
			t.Fatalf("retry status = %d", retry.StatusCode)
		}
		_ = retry.Body.Close()
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	terminal := readModelAuthSnapshot(t, bufio.NewScanner(retry.Body), func(s appserver.ModelAuthenticationSnapshot) bool { return s.Result != nil })
	if terminal.Result.Outcome != appserver.OutcomeUnknown || terminal.ChallengeID != "" || backend.effects.Load() != 1 {
		t.Fatalf("retry changed uncertain outcome: %+v, effects = %d", terminal, backend.effects.Load())
	}
	input, err := http.NewRequestWithContext(ctx, http.MethodPost, host.URL+apiPrefix+"/configuration/operations/connect/auth-input", strings.NewReader(fmt.Sprintf(`{"challenge_id":%q,"input":"late-code"}`, challenge.ChallengeID)))
	if err != nil {
		t.Fatal(err)
	}
	input.Header.Set("Authorization", "owner")
	input.Header.Set("Content-Type", "application/json")
	response, err := host.Client().Do(input)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("canceled authentication input status = %d", response.StatusCode)
	}
}

func TestModelAuthenticationCommittedRetryAndChangedRequestUseNativeLedger(t *testing.T) {
	backend := &modelAuthBackend{run: func(context.Context) (appserver.CommandResult, error) {
		return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 7, Detail: "PRIVATE_PROVIDER_SENTINEL"}, nil
	}}
	host := newModelAuthLedgerServer(t, backend)
	for _, test := range []struct {
		model string
		want  appserver.Outcome
	}{{"grok", appserver.OutcomeCommitted}, {"grok", appserver.OutcomeCommitted}, {"changed-model", appserver.OutcomeConflicted}} {
		response := openModelAuthStream(t, t.Context(), host, "owner", test.model)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("stream status = %d", response.StatusCode)
		}
		snapshot := readModelAuthSnapshot(t, bufio.NewScanner(response.Body), func(s appserver.ModelAuthenticationSnapshot) bool { return s.Result != nil })
		if snapshot.Result.Outcome != test.want || snapshot.Result.Detail != "" || backend.effects.Load() != 1 {
			t.Fatalf("receipt = %+v, effects = %d; want %s", snapshot, backend.effects.Load(), test.want)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
}

func TestModelAuthenticationRejectsApplicationAuthorityAndRedactsInvalidInput(t *testing.T) {
	backend := &modelAuthBackend{}
	host := newModelAuthLedgerServer(t, backend)
	for _, principal := range []string{"", "application"} {
		response := openModelAuthStream(t, t.Context(), host, principal, "grok")
		_ = response.Body.Close()
		want := http.StatusForbidden
		if principal == "" {
			want = http.StatusUnauthorized
		}
		if response.StatusCode != want {
			t.Fatalf("%q stream status = %d, want %d", principal, response.StatusCode, want)
		}
	}
	for _, test := range []struct {
		principal, body string
		want            int
	}{
		{"application", `{"challenge_id":"challenge","input":"SECRET"}`, http.StatusForbidden},
		{"owner", `{"PRIVATE_CODE_SENTINEL":"secret"}`, http.StatusBadRequest},
		{"owner", `{"challenge_id":"challenge","input":true}`, http.StatusBadRequest},
		{"owner", `{"challenge_id":"challenge","input":"` + strings.Repeat("x", 16385) + `"}`, http.StatusBadRequest},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+apiPrefix+"/configuration/operations/connect/auth-input", strings.NewReader(test.body))
		req.Header.Set("Authorization", test.principal)
		req.Header.Set("Content-Type", "application/json")
		response, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != test.want || strings.Contains(string(body), "PRIVATE_CODE_SENTINEL") || strings.Contains(string(body), "SECRET") {
			t.Fatalf("input rejection status = %d, body = %s, error = %v", response.StatusCode, body, err)
		}
	}
	if backend.effects.Load() != 0 {
		t.Fatal("unauthorized authentication dispatched")
	}
}
