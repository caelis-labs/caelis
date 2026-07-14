//go:build windows

package controlclient

// replaceOperationStoreFile uses MOVEFILE_WRITE_THROUGH on Windows. Directory
// handles cannot be flushed through os.File, so there is no additional parent
// directory operation here.
func syncOperationStoreDirectory(string) error {
	return nil
}
