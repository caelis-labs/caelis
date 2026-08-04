package file

type fileOperation string

const (
	fileOperationReplaceDocument      fileOperation = "replace_document"
	fileOperationReplaceTransaction   fileOperation = "replace_transaction"
	fileOperationRemoveTransaction    fileOperation = "remove_transaction"
	fileOperationRemoveRecoveryMarker fileOperation = "remove_recovery_marker"
)
