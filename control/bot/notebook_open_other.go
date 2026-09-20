//go:build !unix

package bot

import "os"

// openNotebookRegular opens rel beneath the confined root and requires a
// regular opened file. Platforms without O_NONBLOCK cannot avoid a blocking
// open on a raced FIFO, but the opened handle is still type-checked before use.
func openNotebookRegular(root *os.Root, rel string) (*os.File, error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errNotebookNotRegular
	}
	return file, nil
}
