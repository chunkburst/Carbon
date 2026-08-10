//go:build windows

package backup

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const backupPrivateFileAllAccess windows.ACCESS_MASK = 0x001F01FF

// os.File.Chmod cannot reduce a Windows DACL. Give the temporary state/config
// file an explicit protected current-user ACL before writing any private bytes.
func secureBackupPrivateTempFile(path string) error {
	user, err := backupCurrentUserSID()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build backup private DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set backup private DACL: %w", err)
	}
	return verifyBackupPrivateFileForUser(path, user)
}

func verifyBackupPrivateFile(path string) error {
	user, err := backupCurrentUserSID()
	if err != nil {
		return err
	}
	return verifyBackupPrivateFileForUser(path, user)
}

func verifyBackupPrivateFileForUser(path string, user *windows.SID) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 1 {
		return errorsNewBackupPrivateACL()
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || (ace.Mask != windows.GENERIC_ALL && ace.Mask != backupPrivateFileAllAccess) {
		return errorsNewBackupPrivateACL()
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(user) {
		return errorsNewBackupPrivateACL()
	}
	return nil
}

func errorsNewBackupPrivateACL() error { return fmt.Errorf("backup private file ACL is unsafe") }

func backupCurrentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close() //nolint:errcheck // the SID below is copied before close
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func replaceBackupPrivateFile(from, to string) error {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncBackupPrivateParent(parent string) error {
	// MOVEFILE_WRITE_THROUGH above is the durable boundary on Windows. Directory
	// Sync is best effort because it is not reliable on every supported filesystem.
	directory, err := os.Open(parent)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
