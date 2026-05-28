//go:build !windows

package acl

import (
	"fmt"
	"runtime"
)

type Mode string

const (
	Grant  Mode = "grant"
	Deny   Mode = "deny"
	Set    Mode = "set"
	Revoke Mode = "revoke"
)

type Rights string

const (
	ReadExecute Rights = "read_execute"
	Traverse    Rights = "traverse"
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

type FileDACLInfo struct {
	Owner           string
	OwnerSID        string
	Protected       bool
	HasDACL         bool
	ACECount        int
	HasInheritedACE bool
}

func ReadFileDACL(string) (Descriptor, error) {
	return Descriptor{}, fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func InspectFileDACL(string) (FileDACLInfo, error) {
	return FileDACLInfo{}, fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func (Descriptor) HasDACL() bool {
	return false
}

func ModifyFileDACL(string, ...Entry) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func RemoveFileDACLPrincipals(string, ...string) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func MissingFileDACLEntries(string, ...Entry) ([]Entry, error) {
	return nil, fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func ReplaceFileDACL(string, bool, ...Entry) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}

func WriteFileDACL(string, Descriptor, bool) error {
	return fmt.Errorf("acl: unsupported on %s", runtime.GOOS)
}
