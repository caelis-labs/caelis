package tuiapp

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

func TestProductExperimentalAPIAndTUISurviveHostRestart(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("configuration invoked provider")
		w.WriteHeader(500)
	}))
	defer provider.Close()
	store, workspace := t.TempDir(), t.TempDir()
	clients, closeHost, err := localclient.New(t.Context(), store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeHost != nil {
			if err := closeHost(); err != nil {
				t.Error(err)
			}
		}
	})
	first, err := clients.Experimental.ExperimentalFeatures(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Async and idle compaction are independent; refresh and retirement are built in.
	if len(first.Features) != 2 || first.Features[0].ID != experimental.AsyncCompact || first.Effective != nil {
		t.Fatalf("discovery=%+v", first)
	}
	for _, v := range first.Desired {
		if v {
			t.Fatal("Core default on")
		}
	}
	revision, err := strconv.ParseUint(first.Revision, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	_, err = clients.Experimental.SetExperimentalFeatures(t.Context(), appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "unknown-feature", ExpectedRevision: &revision}, Values: experimental.Values{"unknown": false}})
	if err == nil {
		t.Fatal("unknown false feature silently accepted")
	}
	m := NewModel(Config{Experimental: clients.Experimental, ExperimentalInitialize: clients.Sessions.Initialize, NoAnimation: true})
	runTeaCmds(t, m, m.openExperimental())
	m.handleExperimentalKey(keyPress("space"))
	runTeaCmds(t, m, m.handleExperimentalKey(keyPress("enter")))
	if m.experimental != nil {
		t.Fatalf("TUI save failed: %+v", m.experimental)
	}
	saved, err := clients.Experimental.ExperimentalFeatures(t.Context(), "")
	if err != nil || !saved.Desired[experimental.AsyncCompact] || saved.Desired[experimental.MCPRefresh] || saved.Desired[experimental.ToolRetirement] {
		t.Fatalf("API readback=%+v %v", saved, err)
	}
	result, err := clients.Experimental.SetExperimentalFeatures(t.Context(), appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "stale-feature", ExpectedRevision: &revision}, Values: experimental.Values{experimental.MCPRefresh: true}})
	if err == nil || result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("stale CAS=%+v %v", result, err)
	}
	if err := closeHost(); err != nil {
		t.Fatal(err)
	}
	closeHost = nil
	clients, closeHost, err = localclient.New(t.Context(), store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewModel(Config{Experimental: clients.Experimental, ExperimentalInitialize: clients.Sessions.Initialize})
	runTeaCmds(t, reopened, reopened.openExperimental())
	if reopened.experimental == nil || !reopened.experimental.draft[experimental.AsyncCompact] || reopened.experimental.draft[experimental.MCPRefresh] {
		t.Fatal("restart lost configuration or TUI drifted")
	}
}
