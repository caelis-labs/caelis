package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

// TestHeadlessRealMimoTerminalBenchStyleParallel is an opt-in product E2E for
// the migrated Headless path. It uses the production HTTP/SSE SessionClient,
// runs two workspaces concurrently, and requires each Turn to inspect its own
// temporary workspace with a terminal tool before returning a script-friendly
// result.
func TestHeadlessRealMimoTerminalBenchStyleParallel(t *testing.T) {
	if strings.TrimSpace(os.Getenv(realMimoE2EEnabledEnv)) != "1" {
		t.Skip("set CAELIS_REAL_MIMO_E2E=1 to run the real-provider Headless E2E")
	}
	sourceStore := strings.TrimSpace(os.Getenv(realMimoE2ESourceStoreEnv))
	if sourceStore == "" {
		t.Fatalf("%s is required", realMimoE2ESourceStoreEnv)
	}
	profileID := strings.TrimSpace(os.Getenv(realMimoE2EProfileEnv))
	if profileID == "" {
		profileID = defaultRealMimoProfile
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e2eStore := t.TempDir()
	profile, effort := copyRealMimoConfiguration(t, ctx, sourceStore, e2eStore, profileID)
	specs := []headlessRealMimoSpec{
		{
			sessionID:    "headless-tbench-sum",
			workspaceKey: "headless-tbench-sum",
			fileName:     "numbers.txt",
			fileContents: "17\n25\n",
			prompt:       "CAELIS_HEADLESS_TBENCH_SUM. Use a terminal command to read numbers.txt and add its integers. Report the final result in the form TBENCH_SUM=<integer>.",
			marker:       "TBENCH_SUM=42",
		},
		{
			sessionID:    "headless-tbench-lines",
			workspaceKey: "headless-tbench-lines",
			fileName:     "items.txt",
			fileContents: "alpha\nbeta\ngamma\ndelta\n",
			prompt:       "CAELIS_HEADLESS_TBENCH_LINES. Use a terminal command to read items.txt and count its non-empty lines. Report the final result in the form TBENCH_LINES=<integer>.",
			marker:       "TBENCH_LINES=4",
		},
	}
	for index := range specs {
		spec := &specs[index]
		spec.cwd = newWorkspaceRuntimeTestDir(
			t,
			spec.workspaceKey,
			"When the user sends the matching CAELIS_HEADLESS_TBENCH prompt, use a terminal command to inspect the named local file. After the tool succeeds, reply with exactly the requested TBENCH_ marker and no other text.",
		)
		if err := os.WriteFile(
			filepath.Join(spec.cwd, spec.fileName),
			[]byte(spec.fileContents),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	stack, err := NewLocalStack(Config{
		StoreDir:           e2eStore,
		WorkspaceKey:       specs[0].workspaceKey,
		WorkspaceCWD:       specs[0].cwd,
		ApprovalMode:       "auto-review",
		PolicyProfile:      "workspace-write",
		ModelProfileID:     profile.ID,
		ModelProfileEffort: effort,
		SkillDirs:          []string{},
		Sandbox:            SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stack.Close(); err != nil {
			t.Errorf("close real Mimo Headless Host: %v", err)
		}
	}()

	remote := newHeadlessRealMimoHTTPClient(t, stack)
	if _, err := remote.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	turns, err := appserver.NewSessionTurnClient(remote)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		result, createErr := remote.CreateSession(ctx, appserver.CreateSessionRequest{
			WriteBase:          appserver.WriteBase{OperationID: "create-" + spec.sessionID},
			PreferredSessionID: spec.sessionID,
			WorkspaceKey:       spec.workspaceKey,
			CWD:                spec.cwd,
			Title:              spec.sessionID,
		})
		if createErr != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("CreateSession(%q) = %#v, %v", spec.sessionID, result, createErr)
		}
	}

	start := make(chan struct{})
	results := make(chan headlessRealMimoResult, len(specs))
	var wait sync.WaitGroup
	for _, spec := range specs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			observation := headlessRealMimoResult{spec: spec}
			result, runErr := headless.RunSessionOnce(
				ctx,
				turns,
				appserver.SessionTurnStartRequest{
					SessionID: spec.sessionID,
					Input:     spec.prompt,
				},
				headless.Options{
					ApprovalPolicy: headless.ApprovalPolicyApproveAll,
					ObserveEnvelope: func(envelope eventstream.Envelope) error {
						observation.envelopes++
						if envelope.Kind == eventstream.KindSessionUpdate && eventstream.UpdateType(envelope.Update) == "tool_call" {
							observation.toolCalls++
						}
						return nil
					},
				},
			)
			observation.result = result
			observation.err = runErr
			results <- observation
		}()
	}
	startedAt := time.Now()
	close(start)
	wait.Wait()
	close(results)

	for observed := range results {
		if observed.err != nil {
			t.Fatalf("Headless(%q): %v", observed.spec.sessionID, observed.err)
		}
		if observed.result.LifecycleState != eventstream.LifecycleStateCompleted ||
			!strings.Contains(observed.result.Output, observed.spec.marker) ||
			observed.result.Target.HandleID == "" ||
			observed.result.Target.RunID == "" ||
			observed.result.Target.TurnID == "" ||
			observed.result.LastCursor == "" ||
			observed.envelopes == 0 ||
			observed.toolCalls == 0 {
			t.Fatalf(
				"Headless(%q) = result:%#v envelopes:%d tool_calls:%d, want %q",
				observed.spec.sessionID,
				observed.result,
				observed.envelopes,
				observed.toolCalls,
				observed.spec.marker,
			)
		}
		foreignMarker := specs[0].marker
		if foreignMarker == observed.spec.marker {
			foreignMarker = specs[1].marker
		}
		if strings.Contains(observed.result.Output, foreignMarker) {
			t.Fatalf(
				"Headless(%q) received foreign workspace output %q",
				observed.spec.sessionID,
				observed.result.Output,
			)
		}
	}
	t.Logf(
		"real Mimo Headless E2E passed: profile=%s Sessions=%d elapsed=%s",
		profile.ID,
		len(specs),
		time.Since(startedAt).Round(time.Millisecond),
	)
}

type headlessRealMimoSpec struct {
	sessionID    string
	workspaceKey string
	cwd          string
	fileName     string
	fileContents string
	prompt       string
	marker       string
}

type headlessRealMimoResult struct {
	spec      headlessRealMimoSpec
	result    headless.Result
	envelopes int
	toolCalls int
	err       error
}

func newHeadlessRealMimoHTTPClient(
	t *testing.T,
	stack *Stack,
) appserver.SessionClient {
	t.Helper()
	const token = "real-mimo-headless-control-token-0123456789"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		appserver.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := controlserver.New(controlserver.HandlerConfig{
		Services:      gatewayTestAppServerServices(stack.ControlClient(), gatewayTestStatusService{}, stack.TaskStreams()),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, server.Handler())
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:       httpServer.URL,
		BearerToken:   token,
		EventBuffer:   256,
		HTTPClient:    httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
}
