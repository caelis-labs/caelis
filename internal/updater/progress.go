package updater

// ProgressStage identifies one user-visible phase of an update.
type ProgressStage string

const (
	ProgressChecking    ProgressStage = "checking"
	ProgressDownloading ProgressStage = "downloading"
	ProgressVerifying   ProgressStage = "verifying"
	ProgressExtracting  ProgressStage = "extracting"
	ProgressInstalling  ProgressStage = "installing"
)

// ProgressEvent reports semantic update progress without prescribing terminal
// rendering. Current and Total are byte counts for download events.
type ProgressEvent struct {
	Stage    ProgressStage
	Detail   string
	Current  int64
	Total    int64
	Done     bool
	Deferred bool
}

type progressReporter func(ProgressEvent)

func reportProgress(report progressReporter, event ProgressEvent) {
	if report != nil {
		report(event)
	}
}
