package tuiapp

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashDisconnectSubmitsMixedProfiles(t *testing.T) {
	service := &modelConnectControlStub{}
	result := slashDisconnectWithContext(context.Background(), service, nil, `["provider:deepseek/flash","acp:codex:model"]`)
	if result.Err != nil || service.deleted != "provider:deepseek/flash,acp:codex:model" {
		t.Fatalf("selection = %q, result = %#v", service.deleted, result)
	}
}

type disconnectWarningControlStub struct {
	modelConnectControlStub
	completed []string
	err       error
}

func (s *disconnectWarningControlStub) DeleteModels(context.Context, []string) ([]string, error) {
	return slices.Clone(s.completed), s.err
}

func TestSlashDisconnectReportsCommittedSelectionAndRefreshesOnWarning(t *testing.T) {
	wantErr := errors.New("session reconciliation warning")
	service := &disconnectWarningControlStub{completed: []string{"deepseek/flash"}, err: wantErr}
	var notices []string
	var refreshed bool
	result := slashDisconnectWithContext(context.Background(), service, func(msg tea.Msg) {
		switch update := msg.(type) {
		case SlashNoticeMsg:
			notices = append(notices, update.Text)
		case statusRefreshRequestMsg:
			refreshed = true
		}
	}, `["provider:deepseek/flash"]`)
	if !errors.Is(result.Err, wantErr) || !result.SuppressTurnDivider || !refreshed {
		t.Fatalf("result = %#v, refreshed = %v", result, refreshed)
	}
	if !slices.Equal(notices, []string{"Disconnected 1 model"}) {
		t.Fatalf("disconnect notices = %#v", notices)
	}
}
