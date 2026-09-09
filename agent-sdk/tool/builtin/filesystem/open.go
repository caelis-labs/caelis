package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

var errNotRegularFile = errors.New("not a regular file")

func openRegularFile(fsys sandbox.FileSystem, path string) (*os.File, error) {
	if fsys == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	file, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrInvalid) {
			return nil, notRegularFileError(path)
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, notRegularFileError(path)
	}
	return file, nil
}

func notRegularFileError(path string) error {
	return tool.WrapError(tool.ErrorCodeInvalidInput, errNotRegularFile, fmt.Sprintf("path %q is not a regular file", path))
}
