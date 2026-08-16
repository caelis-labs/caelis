package tuiapp

import (
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/surfaces/internal/statusbar"
)

type StatusViewModel = statusbar.ViewModel

func statusViewModelFromSnapshot(status controlstatus.StatusSnapshot) StatusViewModel {
	return statusbar.FromSnapshot(status)
}
