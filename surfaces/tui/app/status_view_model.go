package tuiapp

import (
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/surfaces/statusbar"
)

type StatusViewModel = statusbar.ViewModel

func statusViewModelFromSnapshot(status control.StatusSnapshot) StatusViewModel {
	return statusbar.FromSnapshot(status)
}
