package tuiapp

import (
	tuidriver "github.com/OnslaughtSnail/caelis/tui/driver"
	"github.com/OnslaughtSnail/caelis/tui/tuiapp/statusbar"
)

type StatusViewModel = statusbar.ViewModel

func statusViewModelFromSnapshot(status tuidriver.StatusSnapshot) StatusViewModel {
	return statusbar.FromSnapshot(status)
}
