//go:build !windows

package win32

import (
	"errors"
	"os"
)

type FileIdentity struct {
	VolumeSerialNumber uint64   `json:"volume_serial_number"`
	FileID             [16]byte `json:"file_id"`
}

type StableDirectory struct{}

func OpenStableDirectory(string, string) (*StableDirectory, error) {
	return nil, errors.New("win32: stable directory is only available on Windows")
}

func (*StableDirectory) Identity() FileIdentity { return FileIdentity{} }
func (*StableDirectory) Path() string           { return "" }
func (*StableDirectory) OwnerSID() string       { return "" }
func (*StableDirectory) Close() error           { return nil }
func (*StableDirectory) CreateNewFile(string) (*os.File, FileIdentity, error) {
	return nil, FileIdentity{}, errors.New("win32: stable file is only available on Windows")
}
func (*StableDirectory) OpenExpectedFile(string, FileIdentity, string, bool) (*os.File, error) {
	return nil, errors.New("win32: stable file is only available on Windows")
}
func WriteStableFile(*os.File, []byte) error {
	return errors.New("win32: stable file is only available on Windows")
}
