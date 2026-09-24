package controlserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

type authConfiguration struct {
	focusedConfigurationService
	input chan string
}

func (c *authConfiguration) ConnectModel(ctx context.Context, _ appserver.Principal, r appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{Phase: modelconfig.AuthProgressWaitingForBrowser, VerificationURL: "https://example.invalid/login"})
	value, err := modelconfig.RequestAuthInput(ctx, modelconfig.AuthInputRequest{Prompt: "Authorization code", Secret: true})
	if err != nil {
		return appserver.CommandResult{OperationID: r.OperationID, Outcome: appserver.OutcomeUnknown}, err
	}
	c.input <- value
	return appserver.CommandResult{OperationID: r.OperationID, Outcome: appserver.OutcomeCommitted, Revision: 9}, nil
}
func TestModelAuthenticationHTTPStreamInputIsPrincipalBoundAndSingleUse(t *testing.T) {
	cfg := &authConfiguration{input: make(chan string, 1)}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Configuration = cfg
	server, err := New(HandlerConfig{Services: services, Authenticator: AuthenticatorFunc(func(r *http.Request) (appserver.Principal, error) {
		return appserver.Principal{ID: r.Header.Get("Authorization")}, nil
	}), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()
	req, _ := http.NewRequestWithContext(t.Context(), "POST", host.URL+apiPrefix+"/configuration/connect-model", strings.NewReader(`{"operation_id":"connect-1","expected_revision":"1","config":{"provider":"codex","model":"example"}}`))
	req.Header.Set("Authorization", "owner")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Idempotency-Key", "connect-1")
	req.Header.Set("If-Match", `"1"`)
	res, err := host.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("stream status %d: %s", res.StatusCode, b)
	}
	scan := bufio.NewScanner(res.Body)
	challenge := ""
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var snapshot appserver.ModelAuthenticationSnapshot
		if err := wirev1.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.ChallengeID != "" {
			challenge = snapshot.ChallengeID
			break
		}
	}
	if challenge == "" {
		t.Fatal("missing input challenge")
	}
	submit := func(principal, challenge string) int {
		body, _ := json.Marshal(appserver.ModelAuthenticationInput{ChallengeID: challenge, Input: "private-code-sentinel"})
		req, _ := http.NewRequestWithContext(t.Context(), "POST", host.URL+apiPrefix+"/configuration/operations/connect-1/auth-input", strings.NewReader(string(body)))
		req.Header.Set("Authorization", principal)
		req.Header.Set("Content-Type", "application/json")
		response, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, _ := io.ReadAll(response.Body)
		if strings.Contains(string(content), "private-code-sentinel") {
			t.Fatal("secret leaked")
		}
		return response.StatusCode
	}
	if got := submit("other", challenge); got != 409 {
		t.Fatalf("foreign principal: %d", got)
	}
	if got := submit("owner", "old-challenge"); got != 409 {
		t.Fatalf("stale challenge: %d", got)
	}
	if got := submit("owner", challenge); got != 200 {
		t.Fatalf("input: %d", got)
	}
	if got := submit("owner", challenge); got != 409 {
		t.Fatalf("duplicate input: %d", got)
	}
	if got := <-cfg.input; got != "private-code-sentinel" {
		t.Fatal("input was not delivered")
	}
	finished := false
	for scan.Scan() {
		line := scan.Text()
		if strings.Contains(line, "private-code-sentinel") {
			t.Fatal("secret entered stream")
		}
		if strings.Contains(line, `"outcome":"committed"`) {
			finished = true
			if !strings.Contains(line, `"revision":"9"`) {
				t.Fatal("receipt revision did not use decimal wire encoding")
			}
		}
	}
	if !finished {
		t.Fatalf("no terminal receipt: %v", scan.Err())
	}
}
func TestModelAuthenticationInputCancellationRace(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		exchange := &modelAuthExchange{changed: make(chan struct{}, 1)}
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = exchange.requestInput(ctx, modelconfig.AuthInputRequest{Prompt: "Code"}) })
		<-exchange.changed
		snapshot := exchange.read()
		cancel()
		wg.Wait()
		if exchange.submit(appserver.ModelAuthenticationInput{ChallengeID: snapshot.ChallengeID, Input: "late"}) {
			t.Fatal("canceled challenge accepted input")
		}
	}
}

func TestModelAuthenticationChallengeHasOneWinner(t *testing.T) {
	exchange := &modelAuthExchange{changed: make(chan struct{}, 1)}
	input := make(chan string, 1)
	go func() {
		value, _ := exchange.requestInput(t.Context(), modelconfig.AuthInputRequest{Prompt: "Code"})
		input <- value
	}()
	<-exchange.changed
	challenge := exchange.read().ChallengeID
	if _, err := exchange.requestInput(t.Context(), modelconfig.AuthInputRequest{Prompt: "Concurrent code"}); err == nil {
		t.Fatal("second requester displaced the outstanding challenge")
	}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for range 20 {
		wg.Go(func() {
			if exchange.submit(appserver.ModelAuthenticationInput{ChallengeID: challenge, Input: "one-value"}) {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if got := <-input; got != "one-value" || winners.Load() != 1 {
		t.Fatalf("input = %q, accepted responses = %d", got, winners.Load())
	}
	if snapshot := exchange.read(); snapshot.ChallengeID != "" || snapshot.Prompt != "" {
		t.Fatalf("consumed challenge retained: %+v", snapshot)
	}
}

func TestModelAuthenticationProgressCoalescesWithoutAReader(t *testing.T) {
	exchange := &modelAuthExchange{changed: make(chan struct{}, 1)}
	for range 1000 {
		exchange.update(func(s *appserver.ModelAuthenticationSnapshot) { s.Phase = "waiting_for_browser" })
	}
	exchange.update(func(s *appserver.ModelAuthenticationSnapshot) {
		s.Phase = "finished"
		s.Result = &appserver.CommandResult{Outcome: appserver.OutcomeCommitted}
	})
	if len(exchange.changed) != 1 || exchange.read().Sequence != 1001 || exchange.read().Result == nil {
		t.Fatalf("coalescing lost current state: %+v", exchange.read())
	}
}

func TestModelAuthenticationCapabilityIsAddedOnce(t *testing.T) {
	info := appserver.ServerInfo{Capabilities: []string{appserver.CapabilityModelAuthStream}}
	info = applicationServerInfo(info, appserver.AppServerServices{})
	info = applicationServerInfo(info, appserver.AppServerServices{})
	if len(info.Capabilities) != 1 || info.Capabilities[0] != appserver.CapabilityModelAuthStream {
		t.Fatalf("duplicated authentication capability: %+v", info.Capabilities)
	}
}
