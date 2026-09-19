package gatewayapp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

// These tests exercise the production path for one Session service-tier
// selection: Host ConfigurationCommands().UseSessionModel -> frozen placement
// -> sessionconfig.Apply against a real ACP controller handshake -> the real
// Codex adapter (codex.NewBackend + Backend.ServeACP) -> the app-server
// turn/start request. Only the Codex app-server is faked, and every assertion
// reads the captured app-server request.
//
// Each activation is one re-executed test process serving the adapter, so the
// tests also cover remote Thread resume across real adapter process generations.

const (
	// codexTierProfileTiered selects the model whose catalog default is Fast.
	codexTierProfileTiered = "acp:codex:gpt-a"
	// codexTierProfileTierless selects a model advertising no extra tier and
	// declares no session default, so Control must seal Standard itself.
	codexTierProfileTierless = "acp:codex:gpt-b"
	// codexTierProfileUnconfigured declares no speed capability at all.
	codexTierProfileUnconfigured = "acp:codex:gpt-c"
)

type codexTierFixture struct {
	stack     *Stack
	session   session.Session
	principal appserver.Principal
	capture   string
}

// newCodexTierFixture assembles one Host with a single external Codex Agent
// whose endpoint is this test binary acting as the Codex adapter.
func newCodexTierFixture(t *testing.T, config codexTierHelperConfig) *codexTierFixture {
	t.Helper()
	ctx := context.Background()
	stack := newStackForToolTest(t, assembly.ResolvedAssembly{})
	capture := filepath.Join(t.TempDir(), "codex-tier-capture.jsonl")
	config.CapturePath = capture
	config.WorkspaceRoot = stack.composition.workspace.CWD

	connection := controlagents.Connection{
		ID: "codex", Name: "Codex",
		Launcher: controlagents.Launcher{
			Kind:    controlagents.LaunchKindExecutable,
			Command: os.Args[0],
			Args: []string{
				"-test.run=^TestCodexServiceTierAdapterHelperProcess$",
				"--",
				codexTierHelperMarker,
				config.CapturePath,
				config.WorkspaceRoot,
				config.ResumeModel,
				config.ResumeTier,
				rejectFlag(config.RejectFirstTurnAfterResume),
			},
			WorkDir: config.WorkspaceRoot,
		},
	}
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents:      []controlagents.Agent{{ID: "codex", Name: "Codex", ConnectionID: connection.ID}},
	}
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, codexTierProfiles()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	active, err := startGatewayAppTestSession(ctx, stack, "codex-tier-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := stack.sessionRuntimes.acquireRuntimeUse(activated)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return &codexTierFixture{
		stack: stack, session: active, principal: appserver.Principal{ID: stack.composition.authorities.userID},
		capture: capture,
	}
}

func rejectFlag(reject bool) string {
	if reject {
		return "1"
	}
	return "0"
}

// codexTierProfiles maps product speed selection onto the wire tiers the fake
// catalog advertises. Neither speed-capable profile stores a session default,
// so Control must seal the selected tier from the profile's declared speed
// capability alone. The unconfigured profile declares no speed selection.
func codexTierProfiles() []modelprofile.ModelProfile {
	effort := modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}}
	return []modelprofile.ModelProfile{
		{
			ID: codexTierProfileTiered, DisplayName: "Codex Fast default",
			Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
				AgentID: "codex", RemoteModelID: codexTierTieredID,
			}},
			Effort: effort,
			Speed: modelprofile.SpeedCapability{
				DefaultSpeed: "fast", ACPConfigID: codexTierOptionID,
				Choices: []modelprofile.SpeedChoice{
					{Canonical: "standard", WireValue: codexTierStandard},
					{Canonical: "fast", WireValue: codexTierPriority},
				},
			},
		},
		{
			ID: codexTierProfileTierless, DisplayName: "Codex tierless",
			Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
				AgentID: "codex", RemoteModelID: codexTierTierlessID,
			}},
			Effort: effort,
			Speed: modelprofile.SpeedCapability{
				DefaultSpeed: "standard", ACPConfigID: codexTierOptionID,
				Choices: []modelprofile.SpeedChoice{{Canonical: "standard", WireValue: codexTierStandard}},
			},
		},
		{
			ID: codexTierProfileUnconfigured, DisplayName: "Codex unselected",
			Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
				AgentID: "codex", RemoteModelID: codexTierUnconfiguredID,
			}},
			Effort: effort,
		},
	}
}

// useModel selects one configured ACP ModelProfile through the Host command
// surface, exactly as the product does.
func (f *codexTierFixture) useModel(t *testing.T, profileID string, fastMode bool) appserver.CommandResult {
	t.Helper()
	active := mustCurrentSession(t, f.stack, f.session.SessionID)
	result, err := f.stack.ConfigurationCommands().UseSessionModel(context.Background(), f.principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "codex-tier-select-" + uuid.NewString(),
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: profileID, FastMode: fastMode,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(%s, fast=%v) = %#v, %v", profileID, fastMode, result, err)
	}
	return result
}

// runTurn executes one main Turn through the product Session turn client.
func (f *codexTierFixture) runTurn(t *testing.T, input string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err := runHeadlessOnceForGatewayAppTest(ctx, f.stack, f.session, f.session.SessionID, input, headless.Options{})
	return err
}

// TestCodexServiceTierSelectionReachesTurnStart covers explicit tier
// propagation. An explicit Fast or Standard selection must reach turn/start
// even when thread/start reports no serviceTier and the selection equals the
// tier the session already displays, while a Session with no speed selection
// keeps inheriting the remote default.
func TestCodexServiceTierSelectionReachesTurnStart(t *testing.T) {
	cases := []struct {
		name       string
		profileID  string
		fastMode   bool
		wantModel  string
		wantTier   string
		wantTierOn bool
	}{
		{
			// The explicit Fast wire value equals the tier the session already
			// displays from the catalog default, so the displayed value alone
			// cannot prove the selection.
			name: "explicit fast matches catalog default", profileID: codexTierProfileTiered, fastMode: true,
			wantModel: codexTierTieredID, wantTier: codexTierPriority, wantTierOn: true,
		},
		{
			// The tierless model displays Standard as the protocol baseline and
			// declares no session default, so Control must seal Standard.
			name: "explicit standard matches displayed baseline", profileID: codexTierProfileTierless,
			wantModel: codexTierTierlessID, wantTier: codexTierStandard, wantTierOn: true,
		},
		{
			name: "no speed selection inherits remote default", profileID: codexTierProfileUnconfigured,
			wantModel: codexTierUnconfiguredID,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCodexTierFixture(t, codexTierHelperConfig{})
			fixture.useModel(t, test.profileID, test.fastMode)
			if err := fixture.runTurn(t, "report the selected service tier"); err != nil {
				t.Fatalf("Turn error = %v", err)
			}
			turns := fixture.turnStarts(t)
			if len(turns) != 1 {
				t.Fatalf("turn/start requests = %#v, want exactly one", turns)
			}
			turn := turns[0]
			if turn.Model != test.wantModel {
				t.Fatalf("turn/start model = %q, want %q", turn.Model, test.wantModel)
			}
			if turn.ServiceTierSet != test.wantTierOn {
				t.Fatalf("turn/start service tier present = %v, want %v (%#v)", turn.ServiceTierSet, test.wantTierOn, turn)
			}
			if test.wantTierOn && turn.ServiceTier != test.wantTier {
				t.Fatalf("turn/start service tier = %q, want %q", turn.ServiceTier, test.wantTier)
			}
		})
	}
}

// TestCodexServiceTierStandardSurvivesTierlessRemoteResume covers switching one
// ACP Agent from a completed Fast Turn on a tiered remote model to Standard on
// a tierless remote model. The same remote Thread must be resumed and the
// request must carry the newly selected Standard selection, never the previous
// model's Fast tier.
func TestCodexServiceTierStandardSurvivesTierlessRemoteResume(t *testing.T) {
	fixture := newCodexTierFixture(t, codexTierHelperConfig{
		ResumeModel: codexTierTieredID, ResumeTier: codexTierPriority,
	})
	fixture.useModel(t, codexTierProfileTiered, true)
	if err := fixture.runTurn(t, "complete the Fast turn"); err != nil {
		t.Fatalf("Fast Turn error = %v", err)
	}
	fast := fixture.turnStarts(t)
	if len(fast) != 1 || fast[0].Model != codexTierTieredID || fast[0].ServiceTier != codexTierPriority {
		t.Fatalf("Fast Turn requests = %#v, want %s/%s", fast, codexTierTieredID, codexTierPriority)
	}
	remoteSessionID := mustCurrentSession(t, fixture.stack, fixture.session.SessionID).Controller.RemoteSessionID
	if strings.TrimSpace(remoteSessionID) == "" {
		t.Fatal("controller binding lost the remote Session ID")
	}

	fixture.useModel(t, codexTierProfileTierless, false)
	resumed := mustCurrentSession(t, fixture.stack, fixture.session.SessionID)
	if resumed.Controller.AgentName != "codex" || resumed.Controller.RemoteSessionID != remoteSessionID {
		t.Fatalf("Standard selection rebound controller = %#v, want resumed %q", resumed.Controller, remoteSessionID)
	}
	resumes := fixture.capturesFor(t, "thread/resume")
	if len(resumes) == 0 {
		t.Fatalf("Standard selection did not resume the remote Thread: %#v", fixture.capturedMethods(t))
	}
	if resumes[len(resumes)-1].ThreadID != remoteSessionID {
		t.Fatalf("thread/resume Thread = %q, want %q", resumes[len(resumes)-1].ThreadID, remoteSessionID)
	}

	if err := fixture.runTurn(t, "complete the Standard turn"); err != nil {
		t.Fatalf("Standard Turn error = %v", err)
	}
	turns := fixture.turnStarts(t)
	if len(turns) != 2 {
		t.Fatalf("turn/start requests = %#v, want two", turns)
	}
	standard := turns[1]
	if standard.Model != codexTierTierlessID || !standard.ServiceTierSet || standard.ServiceTier != codexTierStandard {
		t.Fatalf("Standard turn/start = %#v, want %s/%s", standard, codexTierTierlessID, codexTierStandard)
	}
}

// TestCodexServiceTierRejectedTurnStartRetriesSelectedStandard covers a backend
// rejection of the first turn/start after the Standard selection. The staged
// selection survives the rejection, so the retry repeats the complete
// Standard request instead of resurrecting the previous model's Fast tier.
func TestCodexServiceTierRejectedTurnStartRetriesSelectedStandard(t *testing.T) {
	fixture := newCodexTierFixture(t, codexTierHelperConfig{
		ResumeModel: codexTierTieredID, ResumeTier: codexTierPriority,
		RejectFirstTurnAfterResume: true,
	})
	fixture.useModel(t, codexTierProfileTiered, true)
	if err := fixture.runTurn(t, "complete the Fast turn"); err != nil {
		t.Fatalf("Fast Turn error = %v", err)
	}
	fixture.useModel(t, codexTierProfileTierless, false)

	if err := fixture.runTurn(t, "rejected Standard turn"); err == nil {
		t.Fatal("rejected turn/start returned no Turn error")
	}
	turns := fixture.turnStarts(t)
	if len(turns) != 2 || !turns[1].Rejected {
		t.Fatalf("turn/start requests = %#v, want a rejected second request", turns)
	}
	if turns[1].Model != codexTierTierlessID || !turns[1].ServiceTierSet || turns[1].ServiceTier != codexTierStandard {
		t.Fatalf("rejected turn/start = %#v, want %s/%s", turns[1], codexTierTierlessID, codexTierStandard)
	}

	if err := fixture.runTurn(t, "retry the Standard turn"); err != nil {
		t.Fatalf("retry Turn error = %v", err)
	}
	turns = fixture.turnStarts(t)
	if len(turns) != 3 {
		t.Fatalf("turn/start requests = %#v, want three", turns)
	}
	retry := turns[2]
	if retry.Model != codexTierTierlessID || !retry.ServiceTierSet || retry.ServiceTier != codexTierStandard {
		t.Fatalf("retried turn/start = %#v, want %s/%s", retry, codexTierTierlessID, codexTierStandard)
	}
}

// readCodexTierRecords reads the append-only evidence written by the adapter
// helper process. A missing capture file means no request reached the
// app-server yet.
func readCodexTierRecords(path string) ([]codexTierCapture, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var records []codexTierCapture
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record codexTierCapture
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (f *codexTierFixture) records(t *testing.T) []codexTierCapture {
	t.Helper()
	records, err := readCodexTierRecords(f.capture)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func (f *codexTierFixture) capturesFor(t *testing.T, method string) []codexTierCapture {
	t.Helper()
	var captured []codexTierCapture
	for _, record := range f.records(t) {
		if record.Method == method {
			captured = append(captured, record)
		}
	}
	return captured
}

func (f *codexTierFixture) turnStarts(t *testing.T) []codexTierCapture {
	t.Helper()
	return f.capturesFor(t, "turn/start")
}

func (f *codexTierFixture) capturedMethods(t *testing.T) []string {
	t.Helper()
	var methods []string
	for _, record := range f.records(t) {
		methods = append(methods, record.Method)
	}
	return methods
}
