package tuiapp

import (
	"github.com/OnslaughtSnail/caelis/surfaces/tui/app/statusbar"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

type StatusViewModel = statusbar.ViewModel

func statusViewModelFromSnapshot(status tuidriver.StatusSnapshot) StatusViewModel {
	return statusbar.FromSnapshot(status)
}
