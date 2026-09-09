//go:build !unix && !windows

package host

import "os"

func openRegularFile(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := rejectNonRegularOpenedFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func rejectNonRegularPlatformFile(*os.File) error {
	return nil
}
