package appserver

const (
	// MaxPromptImageBytes is the largest decoded image admitted in one prompt part.
	MaxPromptImageBytes = 20_000_000
	// MaxPromptImageTotalBytes is the largest decoded image payload admitted in one prompt.
	MaxPromptImageTotalBytes = 32_000_000
)
