//go:build windows

package acl

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
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

const fileDeleteChild windows.ACCESS_MASK = 0x00000040

type Entry struct {
	Principal string
	Rights    Rights
	Mode      Mode
	Inherit   bool
}

type Descriptor struct {
	sd *windows.SECURITY_DESCRIPTOR
}

type FileDACLInfo struct {
	Owner           string
	OwnerSID        string
	Protected       bool
	HasDACL         bool
	ACECount        int
	HasInheritedACE bool
}

func ReadFileDACL(path string) (Descriptor, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Descriptor{}, fmt.Errorf("acl: path is required")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return Descriptor{}, fmt.Errorf("acl: read %s DACL: %w", path, err)
	}
	return Descriptor{sd: sd}, nil
}

func InspectFileDACL(path string) (FileDACLInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return FileDACLInfo{}, fmt.Errorf("acl: path is required")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return FileDACLInfo{}, fmt.Errorf("acl: read %s owner/DACL: %w", path, err)
	}
	info := FileDACLInfo{}
	if owner, _, err := sd.Owner(); err == nil && owner != nil {
		info.OwnerSID = owner.String()
		info.Owner = accountName(owner)
	}
	if control, _, err := sd.Control(); err == nil {
		info.Protected = control&windows.SE_DACL_PROTECTED != 0
	}
	if dacl, _, err := sd.DACL(); err == nil && dacl != nil {
		info.HasDACL = true
		info.ACECount = int(dacl.AceCount)
		info.HasInheritedACE = daclHasInheritedACE(dacl)
	}
	runtime.KeepAlive(sd)
	return info, nil
}

func (d Descriptor) HasDACL() bool {
	if d.sd == nil {
		return false
	}
	_, _, err := d.sd.DACL()
	return err == nil
}

func ModifyFileDACL(path string, entries ...Entry) error {
	current, err := ReadFileDACL(path)
	if err != nil {
		return err
	}
	missing, err := missingEntries(current.sd, entries)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	return writeBuiltFileDACL(path, current.sd, false, missing...)
}

func RemoveFileDACLPrincipals(path string, principals ...string) error {
	if len(principals) == 0 {
		return nil
	}
	entries := make([]Entry, 0, len(principals))
	for _, principal := range principals {
		principal = strings.TrimSpace(principal)
		if principal == "" {
			continue
		}
		entries = append(entries, Entry{Principal: principal, Mode: Revoke})
	}
	if len(entries) == 0 {
		return nil
	}
	current, err := ReadFileDACL(path)
	if err != nil {
		return err
	}
	return writeBuiltFileDACL(path, current.sd, false, entries...)
}

func MissingFileDACLEntries(path string, entries ...Entry) ([]Entry, error) {
	current, err := ReadFileDACL(path)
	if err != nil {
		return nil, err
	}
	return missingEntries(current.sd, entries)
}

func ReplaceFileDACL(path string, protect bool, entries ...Entry) error {
	return writeBuiltFileDACL(path, nil, protect, entries...)
}

func writeBuiltFileDACL(path string, base *windows.SECURITY_DESCRIPTOR, protect bool, entries ...Entry) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("acl: path is required")
	}
	explicit, sids, err := explicitAccessEntries(entries)
	if err != nil {
		return err
	}
	var baseDACL *windows.ACL
	if base != nil {
		baseDACL, _, err = base.DACL()
		if err != nil {
			return fmt.Errorf("acl: extract base %s DACL: %w", path, err)
		}
	}
	nextDACL, err := windows.ACLFromEntries(explicit, baseDACL)
	runtime.KeepAlive(sids)
	if err != nil {
		return fmt.Errorf("acl: build %s DACL: %w", path, err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protect {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, nextDACL, nil); err != nil {
		return fmt.Errorf("acl: write %s DACL: %w", path, err)
	}
	runtime.KeepAlive(base)
	runtime.KeepAlive(nextDACL)
	return nil
}

func WriteFileDACL(path string, descriptor Descriptor, protect bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("acl: path is required")
	}
	if descriptor.sd == nil {
		return fmt.Errorf("acl: descriptor is required")
	}
	dacl, _, err := descriptor.sd.DACL()
	if err != nil {
		return fmt.Errorf("acl: extract %s DACL: %w", path, err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protect {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("acl: write %s DACL: %w", path, err)
	}
	return nil
}

func missingEntries(base *windows.SECURITY_DESCRIPTOR, entries []Entry) ([]Entry, error) {
	if len(entries) == 0 || base == nil {
		return entries, nil
	}
	dacl, _, err := base.DACL()
	if err != nil {
		return nil, err
	}
	missing := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		principal := strings.TrimSpace(entry.Principal)
		if principal == "" {
			continue
		}
		_, sid, err := trustee(principal)
		if err != nil {
			return nil, err
		}
		if sid == nil || !daclHasEntry(dacl, sid, entry) {
			missing = append(missing, entry)
		}
	}
	return missing, nil
}

func daclHasEntry(dacl *windows.ACL, sid *windows.SID, entry Entry) bool {
	if dacl == nil || sid == nil {
		return false
	}
	wantType := uint8(windows.ACCESS_ALLOWED_ACE_TYPE)
	if entry.Mode == Deny {
		wantType = windows.ACCESS_DENIED_ACE_TYPE
	}
	wantMask := mappedFileMask(rightsMask(entry.Rights))
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil {
			continue
		}
		header := (*windows.ACE_HEADER)(unsafe.Pointer(ace))
		if header.AceType != wantType {
			continue
		}
		if entry.Inherit && header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != (windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) {
			continue
		}
		if header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windows.EqualSid(aceSID, sid) {
			continue
		}
		mask := mappedFileMask(ace.Mask)
		if mask&wantMask == wantMask {
			return true
		}
	}
	return false
}

func daclHasInheritedACE(dacl *windows.ACL) bool {
	if dacl == nil {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil {
			continue
		}
		header := (*windows.ACE_HEADER)(unsafe.Pointer(ace))
		if header.AceFlags&windows.INHERITED_ACE != 0 {
			return true
		}
	}
	return false
}

func mappedFileMask(mask windows.ACCESS_MASK) windows.ACCESS_MASK {
	out := mask
	if mask&windows.GENERIC_ALL != 0 {
		out |= windows.STANDARD_RIGHTS_ALL | windows.SPECIFIC_RIGHTS_ALL
	}
	if mask&windows.GENERIC_READ != 0 {
		out |= windows.FILE_GENERIC_READ
	}
	if mask&windows.GENERIC_WRITE != 0 {
		out |= windows.FILE_GENERIC_WRITE
	}
	if mask&windows.GENERIC_EXECUTE != 0 {
		out |= windows.FILE_GENERIC_EXECUTE
	}
	return out
}

func explicitAccessEntries(entries []Entry) ([]windows.EXPLICIT_ACCESS, []*windows.SID, error) {
	if len(entries) == 0 {
		return nil, nil, nil
	}
	out := make([]windows.EXPLICIT_ACCESS, 0, len(entries))
	sids := make([]*windows.SID, 0, len(entries))
	for _, entry := range entries {
		principal := strings.TrimSpace(entry.Principal)
		if principal == "" {
			continue
		}
		trustee, sid, err := trustee(principal)
		if err != nil {
			return nil, nil, err
		}
		if sid != nil {
			sids = append(sids, sid)
		}
		out = append(out, windows.EXPLICIT_ACCESS{
			AccessPermissions: rightsMask(entry.Rights),
			AccessMode:        accessMode(entry.Mode),
			Inheritance:       inheritance(entry.Inherit),
			Trustee:           trustee,
		})
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	return out, sids, nil
}

func trustee(principal string) (windows.TRUSTEE, *windows.SID, error) {
	var (
		sid *windows.SID
		err error
	)
	if strings.HasPrefix(strings.ToUpper(principal), "S-1-") {
		sid, err = windows.StringToSid(principal)
		if err != nil {
			return windows.TRUSTEE{}, nil, fmt.Errorf("acl: parse SID %q: %w", principal, err)
		}
	} else {
		sid, _, _, err = windows.LookupSID("", principal)
		if err != nil {
			return windows.TRUSTEE{}, nil, fmt.Errorf("acl: lookup principal %q: %w", principal, err)
		}
	}
	if sid == nil || !sid.IsValid() {
		return windows.TRUSTEE{}, nil, fmt.Errorf("acl: principal %q has no valid SID", principal)
	}
	return windows.TRUSTEE{
		TrusteeForm:  windows.TRUSTEE_IS_SID,
		TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
		TrusteeValue: windows.TrusteeValueFromSID(sid),
	}, sid, nil
}

func accountName(sid *windows.SID) string {
	if sid == nil {
		return ""
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	account = strings.TrimSpace(account)
	domain = strings.TrimSpace(domain)
	switch {
	case account == "":
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}

func rightsMask(rights Rights) windows.ACCESS_MASK {
	if rights == "" {
		return 0
	}
	switch rights {
	case FullControl:
		return windows.GENERIC_ALL
	case Modify:
		return windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE | fileDeleteChild
	case Write:
		return windows.FILE_WRITE_DATA |
			windows.FILE_APPEND_DATA |
			windows.FILE_WRITE_EA |
			windows.FILE_WRITE_ATTRIBUTES |
			windows.DELETE |
			fileDeleteChild
	case Traverse:
		return windows.FILE_GENERIC_EXECUTE
	case ReadExecute:
		fallthrough
	default:
		return windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
	}
}

func accessMode(mode Mode) windows.ACCESS_MODE {
	switch mode {
	case Deny:
		return windows.DENY_ACCESS
	case Set:
		return windows.SET_ACCESS
	case Revoke:
		return windows.REVOKE_ACCESS
	case Grant:
		fallthrough
	default:
		return windows.GRANT_ACCESS
	}
}

func inheritance(enabled bool) uint32 {
	if !enabled {
		return windows.NO_INHERITANCE
	}
	return windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
}
