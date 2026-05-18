//go:build !windows

package acl

import (
	"fmt"
	"runtime"
)

type Mode string

const (
	Grant Mode = "grant"
	Deny  Mode = "deny"
	Set   Mode = "set"
)

type Rights string

const (
	ReadExecute Rights = "read_execute"
	Write       Rights = "write"
	Modify      Rights = "modify"
	FullControl Rights = "full_control"
)

type Entry struct {
	Principal string
	Rights    Rights
	Mode      Mode
	Inherit   bool
}

type Descriptor struct{}

func ReadFileDACL(string) (Descriptor, error) {
	return Descriptor{}, fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func (Descriptor) HasDACL() bool {
	return false
}

func ModifyFileDACL(string, ...Entry) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func ReplaceFileDACL(string, bool, ...Entry) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func WriteFileDACL(string, Descriptor, bool) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}
