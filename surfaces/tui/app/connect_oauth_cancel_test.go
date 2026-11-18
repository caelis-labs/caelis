package tuiapp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/charmbracelet/x/ansi"
)

type waitingOAuthConnectService struct {
	modelConnectControlStub
	client *httpclient.Client
}

func (s *waitingOAuthConnectService) Connect(ctx context.Context, config controlprompt.ConnectConfig) (status.StatusSnapshot, error) {
	_, err := s.client.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "oauth-test"}, Config: config,
	})
	return status.StatusSnapshot{}, err
}

func TestConnectWizardCancelsWaitingOAuth(t *testing.T) {
	for _, provider := range []string{"codex", "grok"} {
		for _, exit := range []string{"esc", "close", "before dispatch"} {
			t.Run(provider+"/"+exit, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				browserOpened := make(chan struct{})
				opener := func(string) error { close(browserOpened); return nil }
				listen := func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
				headless := func() bool { return false }
				stateDir := t.TempDir()
				var authenticate func(context.Context) error
				if provider == "codex" {
					manager, err := codexauth.NewManager(codexauth.Options{
						CredentialPath: codexauth.DefaultCredentialPath(stateDir), Headless: headless, Listen: listen, BrowserOpener: opener,
					})
					if err != nil {
						t.Fatal(err)
					}
					authenticate = func(ctx context.Context) error {
						return manager.EnsureAuthenticated(ctx, codexauth.LoginOptions{OpenBrowser: true})
					}
				} else {
					manager, err := grokauth.NewManager(grokauth.Options{
						CredentialPath: grokauth.DefaultCredentialPath(stateDir), Headless: headless, Listen: listen, BrowserOpener: opener,
					})
					if err != nil {
						t.Fatal(err)
					}
					authenticate = func(ctx context.Context) error {
						return manager.EnsureAuthenticated(ctx, grokauth.LoginOptions{OpenBrowser: true})
					}
				}
				authDone := make(chan error, 1)
				host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/api/control/v1/configuration/connect-model" {
						http.NotFound(w, r)
						return
					}
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						authDone <- err
						return
					}
					authDone <- authenticate(r.Context())
				}))
				t.Cleanup(host.Close)
				client, err := httpclient.New(httpclient.Config{
					BaseURL: host.URL, BearerToken: "fixture-token", HTTPClient: host.Client(), Compatibility: appserver.CurrentCompatibility(),
				})
				if err != nil {
					t.Fatal(err)
				}
				service := &waitingOAuthConnectService{client: client}
				sender := &ProgramSender{}
				t.Cleanup(sender.Close)
				m := NewModel(ConfigFromControlService(service, sender, Config{
					Context: ctx, Commands: DefaultCommands(), Wizards: DefaultWizards(), PromptRouterFactory: controlprompt.New,
					NoAnimation: true, SlashArgComplete: func(_ context.Context, command, _ string, _ int) ([]SlashArgCandidate, error) {
						switch {
						case command == "connect-options":
							return []SlashArgCandidate{{Value: provider, NoAuth: true}}, nil
						case strings.HasPrefix(command, "connect-model"):
							return []SlashArgCandidate{{Value: "oauth-test-model", ModelMetadataComplete: true, ModelImageInputKnown: true}}, nil
						}
						return nil, nil
					},
				}))
				m.width, m.height, m.ready = 100, 30, true
				runConnectTestCmd(m, m.openSlashArgPicker("connect"))
				connectPress(m, "enter")
				connectPress(m, "space")
				_, submit := m.Update(connectKey("enter"))
				if m.wizardOverlay == nil || !m.wizardOverlay.pending {
					t.Fatal("model selection did not start the connection submission")
				}
				dispatch := submissionDispatchMessageForTest(t, submit)
				if exit == "before dispatch" {
					connectPress(m, "esc")
				}
				_, execute := m.Update(dispatch)
				done := make(chan tea.Msg, 1)
				go func() { done <- execute() }()
				if exit != "before dispatch" {
					select {
					case <-browserOpened:
					case result := <-done:
						t.Fatalf("connect finished before opening the browser: %#v", unwrapSessionViewMessage(result))
					case <-ctx.Done():
						t.Fatal("OAuth did not open the browser")
					}
					if frame := ansi.Strip(m.renderWizardOverlay()); !strings.Contains(frame, "[esc cancel]") {
						t.Fatalf("pending OAuth did not offer cancellation:\n%s", frame)
					}
					if exit == "close" {
						g := m.wizardOverlay.geometry
						point := tea.Mouse{X: g.closeX + 1, Y: g.closeY, Button: tea.MouseLeft}
						connectMouse(m, tea.MouseClickMsg(point))
						connectMouse(m, tea.MouseReleaseMsg(point))
					} else {
						connectPress(m, "esc")
					}
				}
				if m.wizardOverlay != nil {
					t.Fatal("cancel did not close the wizard")
				}
				select {
				case msg := <-done:
					result := unwrapSessionViewMessage(msg).(TaskResultMsg)
					if !result.Interrupted || result.Err != nil {
						t.Fatalf("manual cancel should complete without an error, got %#v", result)
					}
					m.Update(msg)
				case <-ctx.Done():
					t.Fatal("cancel did not release OAuth callback wait")
				}
				if exit == "before dispatch" {
					select {
					case <-browserOpened:
						t.Fatal("cancelled submission opened the browser")
					default:
					}
				} else {
					select {
					case err := <-authDone:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("Host OAuth result = %v, want request cancellation", err)
						}
					case <-ctx.Done():
						t.Fatal("HTTP cancellation did not stop Host OAuth")
					}
				}
				if frame := ansi.Strip(m.View().Content); strings.Contains(frame, "context canceled") || strings.Contains(frame, "connect-model") {
					t.Fatalf("manual cancel leaked a transport error to the frame:\n%s", frame)
				}
				if m.turnRunning() || sender.observationContext(ctx).Err() != nil {
					t.Fatal("cancellation left the TUI busy or stopped observation")
				}
			})
		}
	}
}

func TestWizardOAuthCancellationKeepsOtherFailures(t *testing.T) {
	manual, cancelManual := context.WithCancelCause(t.Context())
	cancelManual(errWizardSubmissionCanceled)
	unexpected, cancelUnexpected := context.WithCancel(t.Context())
	cancelUnexpected()
	wrap := func(err error) error {
		return &url.Error{Op: "Post", URL: "http://127.0.0.1/api/control/v1/configuration/connect-model", Err: err}
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"unexpected cancellation", unexpected, wrap(context.Canceled)},
		{"timeout", manual, wrap(context.DeadlineExceeded)},
		{"connection failure", manual, wrap(io.ErrUnexpectedEOF)},
		{"committed receipt", manual, &appserver.CommandReceiptError{
			Receipt: appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, Err: wrap(errWizardSubmissionCanceled),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if wizardSubmissionCanceledByUser(tc.ctx, tc.err) {
				t.Fatalf("classified %v as manual cancellation", tc.err)
			}
		})
	}
}
