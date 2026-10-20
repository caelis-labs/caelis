package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

type experimentalFixture struct {
	snapshot experimental.Snapshot
	writes   []appserver.ExperimentalRequest
	fail     bool
}

func (f *experimentalFixture) ExperimentalFeatures(context.Context, string) (experimental.Snapshot, error) {
	return f.snapshot, nil
}
func (f *experimentalFixture) SetExperimentalFeatures(_ context.Context, r appserver.ExperimentalRequest) (appserver.CommandResult, error) {
	f.writes = append(f.writes, r)
	if f.fail {
		return appserver.CommandResult{Outcome: appserver.OutcomeUnknown}, errors.New("response lost")
	}
	f.snapshot.Desired = maps.Clone(f.snapshot.Desired)
	maps.Copy(f.snapshot.Desired, r.Values)
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
}
func (f *experimentalFixture) RefreshMCPTools(context.Context, appserver.RefreshMCPToolsRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{}, errors.New("fixture has no MCP catalog")
}
func experimentalModel(t *testing.T) (*Model, *experimentalFixture) {
	t.Helper()
	f := &experimentalFixture{snapshot: experimental.Snapshot{Revision: "7", Scope: "host", AppliesAt: "next_runtime_activation", Features: experimental.Definitions(), Desired: experimental.Complete(experimental.Values{experimental.MCPRefresh: true}), Effective: experimental.Complete(nil)}}
	m := NewModel(Config{Commands: DefaultCommands(), Experimental: f, ExperimentalInitialize: func(context.Context) (appserver.ServerInfo, error) {
		return appserver.ServerInfo{Capabilities: []string{appserver.CapabilityExperimental}}, nil
	}, NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 112, Height: 28})
	runTeaCmds(t, m, m.openExperimental())
	return m, f
}
func TestExperimentalKeyboardSaveErrorAndOriginalReceipt(t *testing.T) {
	m, f := experimentalModel(t)
	if !m.experimental.draft[experimental.MCPRefresh] || m.experimental.draft[experimental.AsyncCompact] {
		t.Fatal("checkbox did not load real configuration")
	}
	_, cmd := m.Update(keyPress("space"))
	runTeaCmds(t, m, cmd)
	if len(f.writes) != 0 {
		t.Fatal("Space saved without Enter")
	}
	m.Update(keyPress("down"))
	m.Update(keyPress("space"))
	m.Update(keyPress("down"))
	m.Update(keyPress("space"))
	f.fail = true
	_, cmd = m.Update(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if m.experimental == nil || m.experimental.err == "" || len(f.writes) != 1 {
		t.Fatal("unknown save reported success")
	}
	original := f.writes[0]
	m.Update(keyPress("space")) // Unresolved payload remains immutable.
	f.fail = false
	_, cmd = m.Update(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if m.experimental != nil || len(f.writes) != 2 || !reflect.DeepEqual(original, f.writes[1]) {
		t.Fatal("retry changed original mutation identity/payload")
	}
	if !f.snapshot.Desired[experimental.AsyncCompact] || f.snapshot.Desired[experimental.MCPRefresh] || !f.snapshot.Desired[experimental.ToolRetirement] {
		t.Fatal("wrong persisted values")
	}
}
func TestExperimentalCommandEscapeAndCapability(t *testing.T) {
	m, f := experimentalModel(t)
	m.Update(keyPress("space"))
	m.Update(keyPress("esc"))
	if m.experimental != nil || len(f.writes) != 0 {
		t.Fatal("Esc saved draft")
	}
	_, cmd := m.submitInteractiveLine("/experimental", "/experimental", nil)
	runTeaCmds(t, m, cmd)
	if m.experimental == nil || m.experimental.draft[experimental.AsyncCompact] {
		t.Fatal("command failed to reopen saved values after discard")
	}
	m.Update(keyPress("esc"))
	m.setInputText("/exper")
	m.refreshSlashCommands()
	if !reflect.DeepEqual(m.slashCandidates, []string{"/experimental"}) || m.slashCommandDisplay("/experimental") != "/experimental" {
		t.Fatalf("canonical completion=%v display=%q", m.slashCandidates, m.slashCommandDisplay("/experimental"))
	}
	runTeaCmds(t, m, m.applySlashCommandCompletion())
	if m.experimental == nil || !m.experimental.loaded {
		t.Fatal("completion did not open the feature editor")
	}
	m.Update(keyPress("esc"))
	m.cfg.ExperimentalInitialize = func(context.Context) (appserver.ServerInfo, error) { return appserver.ServerInfo{}, nil }
	runTeaCmds(t, m, m.openExperimental())
	if m.experimental.loaded || m.experimental.err == "" {
		t.Fatal("unsupported Host silently succeeded")
	}
}
func TestExperimentalRenderedGoldenAndTerminal(t *testing.T) {
	for _, width := range []int{112, 72} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := experimentalModel(t)
			m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
			frame := evalharness.NormalizeFrame(m.renderExperimental()) + "\n"
			path := filepath.Join("testdata", "experimental", fmt.Sprintf("%d.golden", width))
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != frame {
				t.Fatalf("golden differs:\n%s", frame)
			}
			before := m.View().Content
			m.Update(keyPress("down"))
			m.Update(keyPress("space"))
			after := m.View().Content
			assertPhysicalFullscreenFrame(t, width, 30, after, renderFullscreenFramesForTest(t, width, 30, before, after))
			if !strings.Contains(frame, "[x] MCP catalog refresh") {
				t.Fatal("checkbox absent")
			}
		})
	}
}

type overwrittenExperimentalFixture struct{ *experimentalFixture }

func (f overwrittenExperimentalFixture) SetExperimentalFeatures(ctx context.Context, req appserver.ExperimentalRequest) (appserver.CommandResult, error) {
	result, err := f.experimentalFixture.SetExperimentalFeatures(ctx, req)
	f.snapshot.Revision = "9"
	f.snapshot.Desired[experimental.AsyncCompact] = false
	return result, err
}

func TestExperimentalCommittedSaveSupersededBeforeReadback(t *testing.T) {
	m, f := experimentalModel(t)
	m.cfg.Experimental = overwrittenExperimentalFixture{f}
	m.Update(keyPress("space"))
	_, cmd := m.Update(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if m.experimental == nil || m.experimental.err == "" || m.experimental.request != nil {
		t.Fatal("known committed save must keep draft but release completed request")
	}
	if m.experimental.snapshot.Revision != "9" || !m.experimental.draft[experimental.AsyncCompact] {
		t.Fatal("superseded save lost draft or latest baseline")
	}
	frame := evalharness.NormalizeFrame(m.renderExperimental()) + "\n"
	path := filepath.Join("testdata", "experimental", "superseded.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != frame {
		t.Fatalf("superseded save golden differs:\n%s", frame)
	}
	before := m.View().Content
	m.Update(keyPress("down"))
	m.Update(keyPress("space"))
	after := m.View().Content
	assertPhysicalFullscreenFrame(t, 112, 28, after, renderFullscreenFramesForTest(t, 112, 28, before, after))
	if m.experimental.draft[experimental.MCPRefresh] {
		t.Fatal("known completed write left editor frozen")
	}
	m.cfg.Experimental = f
	_, cmd = m.Update(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if m.experimental != nil || len(f.writes) != 2 || f.writes[0].OperationID == f.writes[1].OperationID || *f.writes[1].ExpectedRevision != 9 {
		t.Fatal("new intent did not use new operation and current revision")
	}
}
