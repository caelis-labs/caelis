package tuiapp

import (
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/surfaces/statusbar"
)

type StatusViewModel = statusbar.ViewModel

func statusViewModelFromSnapshot(status control.StatusSnapshot) StatusViewModel {
	return statusbar.FromSnapshot(status)
}
