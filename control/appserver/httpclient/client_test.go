package httpclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestNewRejectsInsecureRemoteOrigin(t *testing.T) {
	if _, err := New(Config{
		BaseURL: "http://control.example.test:7777", BearerToken: "secret",
	}); err == nil {
		t.Fatal("New accepted cleartext non-loopback bearer transport")
	}
	if _, err := New(Config{
		BaseURL: "https://control.example.test", BearerToken: "secret", Compatibility: appserver.CurrentCompatibility(),
	}); err != nil {
		t.Fatalf("New rejected HTTPS remote origin: %v", err)
	}
}

func TestNewRequiresExplicitCompatibilityPolicy(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://control.example.test", BearerToken: "secret"}); err == nil {
		t.Fatal("New accepted an implicit compatibility policy")
	}
}

func TestRemoteStateValidationDoesNotRepeatHostCapabilityHandshake(t *testing.T) {
	client := &Client{compatibility: appserver.CurrentCompatibility("required-host-capability")}
	if err := client.validateRemoteState(appserver.SessionState{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion:      appserver.HTTPAPIVersion,
	}); err != nil {
		t.Fatalf("validateRemoteState() = %v", err)
	}
}

func TestHostHealthAndReadinessUseRootLifecycleEndpoints(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(writer http.ResponseWriter, request *http.Request) {
		status := http.StatusOK
		switch request.URL.Path {
		case "/healthz":
		case "/readyz":
			status = http.StatusServiceUnavailable
		default:
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = fmt.Fprintf(writer, `{"server_id":%q,"instance_id":%q,"ready":false}`,
			appserver.ServerIdentity, "11111111-1111-4111-8111-111111111111")
	})
	defer closeServer()
	health, err := client.Health(context.Background())
	if err != nil || health.Ready {
		t.Fatalf("Health() = %#v, %v", health, err)
	}
	ready, err := client.Readiness(context.Background())
	if err != nil || ready.Ready {
		t.Fatalf("Readiness() = %#v, %v", ready, err)
	}
}

func TestPromptPreservesTypedWriteContract(t *testing.T) {
	var prompted appserver.PromptRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wirev1.APIPrefix + "/initialize":
			writeFixtureJSON(t, w, http.StatusOK, appserver.ServerInfo{
				ProtocolVersion: acpsdk.ProtocolVersionNumber,
				EnvelopeVersion: appserver.EnvelopeVersion,
				APIVersion:      appserver.HTTPAPIVersion,
			})
		case wirev1.APIPrefix + "/sessions/session-1/prompt":
			assertFixtureRequest(t, r, http.MethodPost, "operation-remote-prompt")
			decodeFixtureRequest(t, r, &prompted)
			if prompted.ExpectedRevision == nil {
				t.Fatal("Prompt request omitted expected_revision")
			}
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: "operation-remote-prompt",
				Outcome:     appserver.OutcomeCommitted,
				SessionID:   "session-1",
				Revision:    math.MaxUint64,
				Target: appserver.TurnTarget{
					HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion != acpsdk.ProtocolVersionNumber ||
		info.EnvelopeVersion != appserver.EnvelopeVersion ||
		info.APIVersion != appserver.HTTPAPIVersion {
		t.Fatalf("Initialize result = %#v", info)
	}
	revision := uint64(math.MaxUint64)
	contentParts := []model.ContentPart{
		{Type: model.ContentPartText, Text: "hello "},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "shot.png"},
	}
	result, err := client.Prompt(context.Background(), appserver.PromptRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "operation-remote-prompt",
			SessionID:        "session-1",
			ExpectedRevision: &revision,
		},
		Input:        "hello from Pet",
		ContentParts: contentParts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != math.MaxUint64 ||
		result.Target.HandleID != "handle-1" ||
		result.Target.RunID != "run-1" ||
		result.Target.TurnID != "turn-1" {
		t.Fatalf("Prompt result = %#v", result)
	}
	if prompted.OperationID != "operation-remote-prompt" ||
		prompted.ExpectedRevision == nil ||
		*prompted.ExpectedRevision != math.MaxUint64 ||
		prompted.Input != "hello from Pet" ||
		len(prompted.ContentParts) != 2 ||
		prompted.ContentParts[0] != contentParts[0] ||
		prompted.ContentParts[1] != contentParts[1] {
		t.Fatalf("Prompt request = %#v", prompted)
	}
}

func TestStartParticipantPreservesTypedWriteContract(t *testing.T) {
	var started appserver.StartParticipantRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wirev1.APIPrefix + "/sessions/session-1/participants/start":
			assertFixtureRequest(t, r, http.MethodPost, "operation-remote-start")
			decodeFixtureRequest(t, r, &started)
			if started.ExpectedRevision == nil {
				t.Fatal("StartParticipant request omitted expected_revision")
			}
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID:   "operation-remote-start",
				Outcome:       appserver.OutcomeCommitted,
				SessionID:     "session-1",
				Revision:      math.MaxUint64,
				ParticipantID: "participant-1",
				Target: appserver.TurnTarget{
					HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	revision := uint64(math.MaxUint64)
	result, err := client.StartParticipant(context.Background(), appserver.StartParticipantRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "operation-remote-start",
			SessionID:        "session-1",
			ExpectedRevision: &revision,
		},
		Handle:         "zenith",
		Role:           session.ParticipantRoleSidecar,
		Label:          "@zenith",
		Source:         "slash_profile_zenith",
		Input:          "introduce yourself",
		DisplayAddress: "/zenith",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != math.MaxUint64 || result.ParticipantID != "participant-1" {
		t.Fatalf("StartParticipant result = %#v", result)
	}
	if started.OperationID != "operation-remote-start" ||
		started.ExpectedRevision == nil ||
		*started.ExpectedRevision != math.MaxUint64 ||
		started.Handle != "zenith" ||
		started.Input != "introduce yourself" ||
		started.DisplayAddress != "/zenith" {
		t.Fatalf("StartParticipant request = %#v", started)
	}
}

func TestCreatesCompactsAndClosesSessionThroughTypedFacade(t *testing.T) {
	var created appserver.CreateSessionRequest
	var compacted appserver.CompactSessionRequest
	var closed appserver.CloseSessionRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wirev1.APIPrefix + "/sessions":
			assertFixtureRequest(t, r, http.MethodPost, "operation-create")
			decodeFixtureRequest(t, r, &created)
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: created.OperationID,
				Outcome:     appserver.OutcomeCommitted,
				SessionID:   "session-created",
				Revision:    1,
			})
		case wirev1.APIPrefix + "/sessions/session-created":
			assertFixtureRequest(t, r, http.MethodDelete, "operation-close")
			decodeFixtureRequest(t, r, &closed)
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: closed.OperationID,
				Outcome:     appserver.OutcomeCommitted,
				SessionID:   "session-created",
				Revision:    2,
			})
		case wirev1.APIPrefix + "/sessions/session-created/compact":
			assertFixtureRequest(t, r, http.MethodPost, "operation-compact")
			decodeFixtureRequest(t, r, &compacted)
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: compacted.OperationID,
				Outcome:     appserver.OutcomeCommitted,
				SessionID:   "session-created",
				Revision:    2,
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	createdResult, err := client.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "operation-create"},
		PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdResult.SessionID != "session-created" || created.PreferredSessionID != "session-1" {
		t.Fatalf("CreateSession result/request = %#v / %#v", createdResult, created)
	}

	compactedResult, err := client.CompactSession(context.Background(), appserver.CompactSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "operation-compact",
			SessionID:               "session-created",
			ExpectedControllerEpoch: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compactedResult.SessionID != "session-created" ||
		compacted.OperationID != "operation-compact" ||
		compacted.ExpectedControllerEpoch != "epoch-1" {
		t.Fatalf("CompactSession result/request = %#v / %#v", compactedResult, compacted)
	}

	closedResult, err := client.CloseSession(context.Background(), appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "operation-close",
			SessionID:               "session-created",
			ExpectedControllerEpoch: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closedResult.SessionID != "session-created" ||
		closed.OperationID != "operation-close" ||
		closed.ExpectedControllerEpoch != "epoch-1" {
		t.Fatalf("CloseSession result/request = %#v / %#v", closedResult, closed)
	}
}

func TestReadsSessionStatusThroughTypedFacade(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wirev1.APIPrefix+"/sessions/session-1/status" ||
			r.Method != http.MethodGet ||
			r.URL.Query().Get("workspace_key") != "workspace" ||
			r.URL.Query().Get("cwd") != "/tmp/workspace" ||
			r.URL.Query().Get("surface") != "pet" ||
			r.URL.Query().Get("diagnostics") != "true" {
			t.Fatalf("status request = %s %s", r.Method, r.URL.String())
		}
		writeFixtureJSON(t, w, http.StatusOK, controlstatus.StatusSnapshot{
			Session:     controlstatus.StatusSession{ID: "session-1", Surface: "pet"},
			ModelStatus: controlstatus.StatusModel{Display: "mimo-v2.5-pro"},
		})
	})
	defer closeServer()
	status, err := client.SessionStatus(context.Background(), appserver.StatusRequest{
		SessionID: "session-1", WorkspaceKey: "workspace", CWD: "/tmp/workspace",
		Surface: "pet", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Session.ID != "session-1" || status.Session.Surface != "pet" || status.ModelStatus.Display != "mimo-v2.5-pro" {
		t.Fatalf("status = %#v", status)
	}
}

func TestListSessionsCarriesCanonicalCWD(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wirev1.APIPrefix+"/sessions" ||
			r.Method != http.MethodGet ||
			r.URL.Query().Get("cwd") != "/tmp/workspace" ||
			r.URL.Query().Get("limit") != "20" {
			t.Fatalf("list request = %s %s", r.Method, r.URL.String())
		}
		writeFixtureJSON(t, w, http.StatusOK, session.SessionList{})
	})
	defer closeServer()
	if _, err := client.ListSessions(context.Background(), appserver.ListSessionsRequest{
		CWD: "/tmp/workspace", Limit: 20,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostFocusedRequestsUseUnscopedRoutes(t *testing.T) {
	requests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: wirev1.APIPrefix + "/status"},
		{method: http.MethodPost, path: wirev1.APIPrefix + "/completion/skills"},
		{method: http.MethodPost, path: wirev1.APIPrefix + "/configuration/use-model"},
		{method: http.MethodPost, path: wirev1.APIPrefix + "/configuration/sandbox-reset"},
	}
	index := 0
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if index >= len(requests) || r.Method != requests[index].method || r.URL.Path != requests[index].path {
			t.Fatalf("Host focused request[%d] = %s %s", index, r.Method, r.URL.Path)
		}
		switch index {
		case 2:
			assertFixtureRequest(t, r, http.MethodPost, "model-use-1")
			if got := r.Header.Get("If-Match"); got != `"4"` {
				t.Fatalf("model If-Match = %q, want quoted revision", got)
			}
			var request appserver.UseModelRequest
			decodeFixtureRequest(t, r, &request)
			if request.SessionID != "" || request.ExpectedRevision == nil || *request.ExpectedRevision != 4 || request.Model != "mimo" {
				t.Fatalf("model wire request = %#v", request)
			}
		case 3:
			assertFixtureRequest(t, r, http.MethodPost, "sandbox-reset-1")
			if got := r.Header.Get("If-Match"); got != `"4"` {
				t.Fatalf("sandbox If-Match = %q, want quoted revision", got)
			}
			var request appserver.SandboxRequest
			decodeFixtureRequest(t, r, &request)
			if request.SessionID != "" || request.ExpectedRevision == nil || *request.ExpectedRevision != 4 {
				t.Fatalf("sandbox wire request = %#v", request)
			}
		}
		switch index {
		case 0:
			writeFixtureJSON(t, w, http.StatusOK, controlstatus.StatusSnapshot{})
		case 2:
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: "model-use-1", Outcome: appserver.OutcomeCommitted, Revision: 5,
			})
		case 3:
			writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: "sandbox-reset-1", Outcome: appserver.OutcomeCommitted, Revision: 4,
			})
		default:
			writeFixtureJSON(t, w, http.StatusOK, []appserver.CompletionCandidate{})
		}
		index++
	})
	defer closeServer()
	if _, err := client.SessionStatus(context.Background(), appserver.StatusRequest{Surface: "tui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CompleteSkill(context.Background(), appserver.CompletionRequest{Surface: "tui"}); err != nil {
		t.Fatal(err)
	}
	modelRevision := uint64(4)
	if _, err := client.UseModel(context.Background(), appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "model-use-1", ExpectedRevision: &modelRevision},
		Model:     "mimo",
	}); err != nil {
		t.Fatal(err)
	}
	expectedRevision := uint64(4)
	if _, err := client.ResetSandbox(context.Background(), appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-reset-1", ExpectedRevision: &expectedRevision,
	}}); err != nil {
		t.Fatal(err)
	}
	if index != len(requests) {
		t.Fatalf("Host focused requests = %d, want %d", index, len(requests))
	}
}

func TestPreservesConflictedCommandRecoveryResult(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFixtureRequest(t, r, http.MethodPost, "operation-conflict")
		writeFixtureJSON(t, w, http.StatusConflict, appserver.CommandResult{
			OperationID: "operation-conflict",
			Outcome:     appserver.OutcomeConflicted,
			SessionID:   "session-1",
			Revision:    9,
			Detail:      "conflict",
		})
	})
	defer closeServer()

	result, err := client.Prompt(context.Background(), appserver.PromptRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "operation-conflict",
			SessionID:   "session-1",
		},
		Input: "conflict",
	})
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) ||
		outcomeErr.Outcome != appserver.OutcomeConflicted ||
		result.Outcome != appserver.OutcomeConflicted ||
		result.Revision != 9 {
		t.Fatalf("Prompt conflict = %#v, %v", result, err)
	}
}

func TestPromptReportsRemoteHostUnavailable(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFixtureRequest(t, r, http.MethodPost, "operation-unavailable")
		writeFixtureJSON(t, w, http.StatusServiceUnavailable, map[string]string{"error": "service unavailable"})
	})
	defer closeServer()

	result, err := client.Prompt(context.Background(), appserver.PromptRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "operation-unavailable",
			SessionID:   "session-1",
		},
		Input: "retry after restart",
	})
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Prompt error = %T %v, want HTTP 503 RemoteError", err, err)
	}
	if errorcode.CodeOf(err) != errorcode.Unavailable {
		t.Fatalf("Prompt error code = %q, want unavailable", errorcode.CodeOf(err))
	}
	if result != (appserver.CommandResult{}) {
		t.Fatalf("Prompt result = %#v, want zero result", result)
	}
}

func TestFocusedMutationPreservesRemoteSessionClosed(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != wirev1.APIPrefix+"/sessions/session-1/configuration/session-mode" {
			t.Fatalf("focused request = %s %s", r.Method, r.URL.Path)
		}
		writeFixtureJSON(t, w, http.StatusConflict, map[string]string{
			"error": "conflict",
			"code":  string(errorcode.FailedPrecondition),
			"kind":  string(appserver.ErrorKindSessionClosed),
		})
	})
	defer closeServer()

	revision := uint64(1)
	_, err := client.ConfigureSessionMode(context.Background(), appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{OperationID: "mode-1", SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"},
		Mode:      "manual",
	})
	if !errors.Is(err, appserver.ErrSessionClosed) || errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("focused closed error = %T %v (code %q), want ErrSessionClosed", err, err, errorcode.CodeOf(err))
	}
}

func TestReconnectReturnsTypedAtomicSubscription(t *testing.T) {
	backfill := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-backfill", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq: math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "replayed"},
		},
		Meta: map[string]any{"compact": map[string]any{
			"summarized_through_seq": uint64(math.MaxUint64),
		}},
	}
	live := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-live", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor:     eventstream.DurableFeedPosition{Seq: math.MaxUint64},
			Generation: "generation-1",
			Sequence:   math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage,
			Size:          math.MaxUint64,
			Used:          math.MaxUint64,
		},
	}
	state := appserver.SessionState{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion:      appserver.HTTPAPIVersion,
		SessionID:       "session-1",
		Revision:        math.MaxUint64,
		ResumeMode:      appserver.ResumeModeExact,
		BoundaryCursor:  "cursor-boundary",
		Controller: session.ControllerBinding{
			ContextSyncSeq: math.MaxUint64,
		},
	}
	client, closeServer := newFixtureClient(t, reconnectFixture(t, state, []eventstream.Envelope{backfill}, []eventstream.Envelope{live}, true))
	defer closeServer()

	result, err := client.Reconnect(context.Background(), appserver.ReconnectRequest{
		SessionID: "session-1", Cursor: "cursor-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.State.Revision != math.MaxUint64 ||
		result.State.Controller.ContextSyncSeq != math.MaxUint64 ||
		result.State.BoundaryCursor != "cursor-boundary" {
		t.Fatalf("Reconnect state = %#v", result.State)
	}

	replayed := receiveRemoteEnvelope(t, result.Subscription.Backfill())
	if replayed.Cursor != "cursor-backfill" ||
		replayed.Position == nil ||
		replayed.Position.Durable == nil ||
		replayed.Position.Durable.Seq != math.MaxUint64 {
		t.Fatalf("backfill Envelope = %#v", replayed)
	}
	compact, ok := replayed.Meta["compact"].(map[string]any)
	if !ok || compact["summarized_through_seq"] != uint64(math.MaxUint64) {
		t.Fatalf("backfill metadata = %#v", replayed.Meta)
	}
	if _, ok := replayed.Update.(eventstream.ContentChunk); !ok {
		t.Fatalf("backfill update = %T", replayed.Update)
	}
	select {
	case <-result.Subscription.BackfillDone():
	case <-time.After(2 * time.Second):
		t.Fatal("backfill marker was not delivered")
	}
	continued := receiveRemoteEnvelope(t, result.Subscription.Events())
	usage, ok := continued.Update.(eventstream.UsageUpdate)
	if !ok || usage.Size != math.MaxUint64 || usage.Used != math.MaxUint64 ||
		continued.Position == nil ||
		continued.Position.Transient == nil ||
		continued.Position.Transient.Sequence != math.MaxUint64 {
		t.Fatalf("live Envelope = %#v", continued)
	}
	if result.Subscription.LastCursor() != "cursor-live" {
		t.Fatalf("LastCursor = %q", result.Subscription.LastCursor())
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("subscription error = %v", err)
	}
}

func TestRemoteSubscriptionLastCursorLinearWithDelivery(t *testing.T) {
	// Under concurrency, receiving an event must not observe a stale LastCursor.
	for range 200 {
		reader, writer := io.Pipe()
		response := &http.Response{Body: reader}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
		subscription := newRemoteSubscription(response, scanner, 8, "cursor-client")

		_, _ = io.WriteString(writer, "id: cursor-backfill\ndata: {\"kind\":\"notice\",\"cursor\":\"cursor-backfill\",\"session_id\":\"s1\",\"notice\":\"b\"}\n\n")
		select {
		case envelope := <-subscription.Backfill():
			if envelope.Cursor != "cursor-backfill" {
				t.Fatalf("backfill envelope = %#v", envelope)
			}
			if got := subscription.LastCursor(); got != "cursor-backfill" {
				t.Fatalf("LastCursor() = %q immediately after backfill, want cursor-backfill", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for backfill")
		}

		_, _ = io.WriteString(writer, "event: "+wirev1.BackfillDoneEventName+"\ndata: {}\n\n")
		select {
		case <-subscription.BackfillDone():
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for backfill marker")
		}

		_, _ = io.WriteString(writer, "id: cursor-live\ndata: {\"kind\":\"notice\",\"cursor\":\"cursor-live\",\"session_id\":\"s1\",\"notice\":\"l\"}\n\n")
		select {
		case envelope := <-subscription.Events():
			if envelope.Cursor != "cursor-live" {
				t.Fatalf("live envelope = %#v", envelope)
			}
			if got := subscription.LastCursor(); got != "cursor-live" {
				t.Fatalf("LastCursor() = %q immediately after live receive, want cursor-live", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for live event")
		}
		_ = writer.Close()
		_ = subscription.Close()
	}
}

func TestReconnectDisconnectsSlowConsumerWithCursor(t *testing.T) {
	backfill := make([]eventstream.Envelope, 4)
	for index := range backfill {
		backfill[index] = eventstream.Envelope{
			Kind: eventstream.KindNotice, Cursor: "cursor-" + strconv.Itoa(index+1),
			SessionID: "session-1", Notice: "event",
		}
	}
	state := appserver.SessionState{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion:      appserver.HTTPAPIVersion,
		SessionID:       "session-1",
		ResumeMode:      appserver.ResumeModeExact,
	}
	client, closeServer := newFixtureClientWithConfig(t, Config{EventBuffer: 1}, reconnectFixture(t, state, backfill, nil, false))
	defer closeServer()

	result, err := client.Reconnect(context.Background(), appserver.ReconnectRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case <-result.Subscription.BackfillDone():
	case <-time.After(2 * time.Second):
		t.Fatal("slow-consumer subscription did not terminate")
	}
	var gap *appserver.FeedGapError
	if !errors.As(result.Subscription.Err(), &gap) ||
		!errors.Is(gap, appserver.ErrSlowConsumer) ||
		gap.RetryCursor != "cursor-1" {
		t.Fatalf("subscription error = %#v", result.Subscription.Err())
	}
}

func newFixtureClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	return newFixtureClientWithConfig(t, Config{}, handler)
}

func newFixtureClientWithConfig(t *testing.T, config Config, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	config.BaseURL = "http://127.0.0.1"
	config.BearerToken = "test-token"
	config.HTTPClient = &http.Client{Transport: fixtureRoundTripper{handler: handler}}
	if err := config.Compatibility.Validate(); err != nil {
		config.Compatibility = appserver.CurrentCompatibility()
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client, func() {}
}

func assertFixtureRequest(t *testing.T, request *http.Request, method, operationID string) {
	t.Helper()
	if request.Method != method {
		t.Errorf("method = %q, want %q", request.Method, method)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get("Idempotency-Key"); got != operationID {
		t.Errorf("Idempotency-Key = %q, want %q", got, operationID)
	}
}

func writeFixtureJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	raw, err := wirev1.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func decodeFixtureRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := wirev1.DecodeRequest(raw, target); err != nil {
		t.Fatal(err)
	}
}

func reconnectFixture(
	t *testing.T,
	state appserver.SessionState,
	backfill []eventstream.Envelope,
	live []eventstream.Envelope,
	holdOpen bool,
) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != wirev1.APIPrefix+"/sessions/session-1/reconnect" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if cursor := request.URL.Query().Get("after"); cursor != "" && cursor != "cursor-client" {
			t.Errorf("after = %q", cursor)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeFixtureSSE(t, writer, wirev1.BootstrapEventName, "", state)
		for _, envelope := range backfill {
			writeFixtureSSE(t, writer, "", envelope.Cursor, envelope)
		}
		writeFixtureSSE(t, writer, wirev1.BackfillDoneEventName, "", map[string]any{})
		for _, envelope := range live {
			writeFixtureSSE(t, writer, "", envelope.Cursor, envelope)
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if holdOpen {
			<-request.Context().Done()
		}
	}
}

func writeFixtureSSE(t *testing.T, writer http.ResponseWriter, event, id string, value any) {
	t.Helper()
	var raw []byte
	var err error
	if envelope, ok := value.(eventstream.Envelope); ok {
		raw, err = wirev1.MarshalEnvelope(envelope)
	} else {
		raw, err = wirev1.Marshal(value)
	}
	if err != nil {
		t.Fatal(err)
	}
	if event != "" {
		_, _ = fmt.Fprintf(writer, "event: %s\n", event)
	}
	if id != "" {
		_, _ = fmt.Fprintf(writer, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", raw)
}

func receiveRemoteEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case envelope, ok := <-events:
		if !ok {
			t.Fatal("remote Envelope channel closed")
		}
		return envelope
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote Envelope")
		return eventstream.Envelope{}
	}
}

type fixtureRoundTripper struct {
	handler http.Handler
}

func (roundTripper fixtureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestContext, cancel := context.WithCancel(request.Context())
	clonedRequest := request.Clone(requestContext)
	reader, writer := io.Pipe()
	responseWriter := &fixtureResponseWriter{
		header: make(http.Header),
		body:   writer,
		ready:  make(chan struct{}),
	}
	go func() {
		defer responseWriter.finish()
		roundTripper.handler.ServeHTTP(responseWriter, clonedRequest)
	}()

	select {
	case <-request.Context().Done():
		cancel()
		_ = reader.CloseWithError(request.Context().Err())
		_ = writer.CloseWithError(request.Context().Err())
		return nil, request.Context().Err()
	case <-responseWriter.ready:
		return &http.Response{
			StatusCode: responseWriter.statusCode,
			Header:     responseWriter.header.Clone(),
			Body: &fixtureResponseBody{
				ReadCloser: reader,
				cancel:     cancel,
			},
			Request: clonedRequest,
		}, nil
	}
}

type fixtureResponseWriter struct {
	header     http.Header
	body       *io.PipeWriter
	ready      chan struct{}
	readyOnce  sync.Once
	statusCode int
}

func (writer *fixtureResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *fixtureResponseWriter) WriteHeader(statusCode int) {
	writer.readyOnce.Do(func() {
		writer.statusCode = statusCode
		close(writer.ready)
	})
}

func (writer *fixtureResponseWriter) Write(data []byte) (int, error) {
	writer.WriteHeader(http.StatusOK)
	return writer.body.Write(data)
}

func (writer *fixtureResponseWriter) Flush() {
	writer.WriteHeader(http.StatusOK)
}

func (writer *fixtureResponseWriter) finish() {
	writer.WriteHeader(http.StatusOK)
	_ = writer.body.Close()
}

type fixtureResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *fixtureResponseBody) Close() error {
	body.cancel()
	return body.ReadCloser.Close()
}
