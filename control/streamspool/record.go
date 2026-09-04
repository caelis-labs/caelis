package streamspool

const (
	// FormatVersion is the on-disk segment/record format version.
	FormatVersion uint16 = 1
	// MaximumRecordBytes is the default encoded payload safety limit.
	MaximumRecordBytes = 4 << 20
)
