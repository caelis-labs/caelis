package updater

// ProgressStage identifies one user-visible phase of an update.
//
// Raw self-update delegates download, checksum verification, extraction, and
// binary replacement to the official installer published at caelis.dev, so it
// reports only the outer checking and installation phases.
type ProgressStage string

const (
	ProgressChecking   ProgressStage = "checking"
	ProgressInstalling ProgressStage = "installing"
)

// ProgressEvent reports semantic update progress without prescribing terminal
// rendering.
type ProgressEvent struct {
	Stage    ProgressStage
	Detail   string
	Done     bool
	Deferred bool
}

type progressReporter func(ProgressEvent)

func reportProgress(report progressReporter, event ProgressEvent) {
	if report != nil {
		report(event)
	}
}
