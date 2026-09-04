package tuiapp

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashDisconnectSubmitsMultipleSelections(t *testing.T) {
	provider := &modelConnectControlStub{}
	result := slashDisconnectWithContext(context.Background(), provider, nil, "provider deepseek/flash, openai-codex/luna,deepseek/flash")
	if result.Err != nil || provider.deleted != "deepseek/flash,openai-codex/luna" {
		t.Fatalf("provider selection = %q, result = %#v", provider.deleted, result)
	}
	acp := &acpConnectControlStub{}
	result = slashDisconnectWithContext(context.Background(), acp, nil, "acp codex,grok")
	if result.Err != nil || acp.disconnected != "codex,grok" {
		t.Fatalf("ACP selection = %q, result = %#v", acp.disconnected, result)
	}
}

type partialDisconnectControlStub struct {
	modelConnectControlStub
	completed []string
	err       error
}

func (s *partialDisconnectControlStub) DeleteModels(context.Context, []string) ([]string, error) {
	return slices.Clone(s.completed), s.err
}

func TestSlashDisconnectReportsCompletedSelectionsAndRefreshesOnError(t *testing.T) {
	wantErr := errors.New("second model removal outcome unknown")
	service := &partialDisconnectControlStub{completed: []string{"deepseek/flash"}, err: wantErr}
	var notices []string
	var refreshed bool
	result := slashDisconnectWithContext(context.Background(), service, func(msg tea.Msg) {
		switch update := msg.(type) {
		case SlashNoticeMsg:
			notices = append(notices, update.Text)
		case SetCommandsMsg:
			refreshed = true
		}
	}, "provider deepseek/flash,openai-codex/luna,openai-codex/astra")
	if !errors.Is(result.Err, wantErr) || !result.SuppressTurnDivider || !refreshed {
		t.Fatalf("result = %#v, refreshed = %v", result, refreshed)
	}
	if !slices.Equal(notices, []string{"Disconnected deepseek/flash"}) {
		t.Fatalf("disconnect notices = %#v", notices)
	}
}
