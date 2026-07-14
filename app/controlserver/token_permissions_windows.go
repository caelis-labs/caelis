//go:build windows

package controlserver

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureTokenFile(file *os.File) error {
	user, system, administrators, err := tokenFileSIDs()
	if err != nil {
		return err
	}
	sids := []*windows.SID{user, system, administrators}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build bearer token DACL: %w", err)
	}
	info := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT, info, user, nil, dacl, nil); err != nil {
		return fmt.Errorf("set bearer token owner/DACL: %w", err)
	}
	runtime.KeepAlive(entries)
	runtime.KeepAlive(sids)
	runtime.KeepAlive(dacl)
	return nil
}

func validateTokenFileSecurity(file *os.File, _ os.FileInfo) error {
	user, system, administrators, err := tokenFileSIDs()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("controlserver: read bearer token owner/DACL: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user) {
		return fmt.Errorf("controlserver: bearer token file is not owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("controlserver: bearer token file DACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("controlserver: bearer token file DACL is unavailable")
	}
	allowed := []*windows.SID{user, system, administrators}
	var userAccess bool
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return fmt.Errorf("controlserver: read bearer token DACL entry %d", index)
		}
		header := (*windows.ACE_HEADER)(unsafe.Pointer(ace))
		if header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || header.AceFlags&windows.INHERITED_ACE != 0 {
			return fmt.Errorf("controlserver: bearer token DACL contains an unexpected entry")
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !tokenFileSIDAllowed(allowed, aceSID) {
			return fmt.Errorf("controlserver: bearer token DACL grants an unexpected principal")
		}
		if windows.EqualSid(aceSID, user) && tokenFileAccessMask(ace.Mask)&(windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE) == (windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE) {
			userAccess = true
		}
	}
	runtime.KeepAlive(sd)
	if !userAccess {
		return fmt.Errorf("controlserver: bearer token DACL does not grant owner read/write access")
	}
	return nil
}

func tokenFileSIDs() (*windows.SID, *windows.SID, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("controlserver: resolve current Windows user SID: %w", err)
	}
	if user == nil || user.User.Sid == nil {
		return nil, nil, nil, fmt.Errorf("controlserver: current Windows user SID is unavailable")
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("controlserver: resolve LocalSystem SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("controlserver: resolve Administrators SID: %w", err)
	}
	return user.User.Sid, system, administrators, nil
}

func tokenFileSIDAllowed(allowed []*windows.SID, candidate *windows.SID) bool {
	for _, sid := range allowed {
		if sid != nil && candidate != nil && windows.EqualSid(sid, candidate) {
			return true
		}
	}
	return false
}

func tokenFileAccessMask(mask windows.ACCESS_MASK) windows.ACCESS_MASK {
	if mask&windows.GENERIC_ALL != 0 {
		mask |= windows.STANDARD_RIGHTS_ALL | windows.SPECIFIC_RIGHTS_ALL
	}
	if mask&windows.GENERIC_READ != 0 {
		mask |= windows.FILE_GENERIC_READ
	}
	if mask&windows.GENERIC_WRITE != 0 {
		mask |= windows.FILE_GENERIC_WRITE
	}
	return mask
}

// Windows does not expose a portable directory fsync operation. The token
// file itself is flushed before the NTFS no-clobber hard-link publication.
func syncTokenDirectory(string) error { return nil }
