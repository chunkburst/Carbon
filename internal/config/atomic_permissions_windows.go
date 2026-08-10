//go:build windows

package config

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const atomicFileAllAccess windows.ACCESS_MASK = 0x001F01FF

// os.File.Chmod does not reduce a Windows DACL. The temp file therefore receives an
// explicit protected DACL for the current process user before config bytes are written.
// SetSecurityInfo targets the open handle so a temporary pathname race cannot redirect
// the DACL update to another file.
func secureAtomicTempFile(file *os.File) error {
	user, err := atomicCurrentUserSID()
	if err != nil {
		return err
	}
	handle, err := openAtomicSecurityHandle(file)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // no recovery action after DACL setup
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
		return fmt.Errorf("build private DACL: %w", err)
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("set protected private DACL: %w", err)
	}
	return verifyAtomicPrivateHandleForUser(handle, user)
}

// openAtomicSecurityHandle obtains WRITE_DAC on the staged file without following a
// possible reparse point. The normal os.CreateTemp handle lacks WRITE_DAC on Windows;
// comparing the original and reopened file IDs rejects a path swap before ACL mutation.
func openAtomicSecurityHandle(file *os.File) (windows.Handle, error) {
	path, err := atomicWindowsPath(file.Name())
	if err != nil {
		return 0, err
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(file)
	if err != nil {
		return 0, fmt.Errorf("open temp security handle: %w", err)
	}
	var original, reopened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &original); err != nil {
		windows.CloseHandle(handle) //nolint:errcheck // reporting original inspection error
		return 0, fmt.Errorf("inspect original temp handle: %w", err)
	}
	if err := windows.GetFileInformationByHandle(handle, &reopened); err != nil {
		windows.CloseHandle(handle) //nolint:errcheck // reporting reopened inspection error
		return 0, fmt.Errorf("inspect reopened temp handle: %w", err)
	}
	if reopened.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !sameAtomicFileIdentity(original, reopened) {
		windows.CloseHandle(handle) //nolint:errcheck // rejecting unsafe temp-name target
		return 0, fmt.Errorf("temporary path no longer names the opened regular file")
	}
	return handle, nil
}

func sameAtomicFileIdentity(left, right windows.ByHandleFileInformation) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber &&
		left.FileIndexHigh == right.FileIndexHigh &&
		left.FileIndexLow == right.FileIndexLow
}

type atomicTempIdentity struct {
	info windows.ByHandleFileInformation
}

func captureAtomicTempIdentity(file *os.File) (atomicTempIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return atomicTempIdentity{}, err
	}
	runtime.KeepAlive(file)
	return atomicTempIdentity{info: info}, nil
}

// validateAtomicTempFile verifies that the temp name still resolves to the exact file
// whose bytes were written and synced. The reopen uses OPEN_REPARSE_POINT, so an
// introduced junction/symlink cannot be followed during the comparison.
func validateAtomicTempFile(path string, expected atomicTempIdentity) error {
	if err := validateAtomicTemp(path); err != nil {
		return err
	}
	openedPath, err := atomicWindowsPath(path)
	if err != nil {
		return err
	}
	ptr, err := windows.UTF16PtrFromString(openedPath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open named temp file: %w", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // no recovery action after comparison
	var named windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &named); err != nil {
		return fmt.Errorf("inspect named temp file: %w", err)
	}
	if named.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !sameAtomicFileIdentity(expected.info, named) {
		return fmt.Errorf("temporary path no longer names the opened regular file")
	}
	return nil
}

// removeAtomicTempFile preserves an unknown entry if the staging file identity no
// longer matches, avoiding cleanup-induced deletion of a substituted pathname.
func removeAtomicTempFile(path string, expected atomicTempIdentity) error {
	if err := validateAtomicTempFile(path, expected); err != nil {
		return nil
	}
	return os.Remove(path)
}

func verifyAtomicPrivateFile(path string) error {
	user, err := atomicCurrentUserSID()
	if err != nil {
		return err
	}
	return verifyAtomicPrivateFileForUser(path, user)
}

func verifyAtomicPrivateFileForUser(path string, user *windows.SID) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	return verifyAtomicPrivateSecurityDescriptor(sd, user)
}

func verifyAtomicPrivateHandleForUser(handle windows.Handle, user *windows.SID) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	return verifyAtomicPrivateSecurityDescriptor(sd, user)
}

func verifyAtomicPrivateSecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("read DACL control flags: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("private DACL is not protected from inherited ACEs")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read DACL entries: %w", err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("expected exactly one protected user ACE, got %d", aceCount(dacl))
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read private DACL ACE: %w", err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || (ace.Mask != windows.GENERIC_ALL && ace.Mask != atomicFileAllAccess) {
		return fmt.Errorf("unexpected private DACL ACE type=%d mask=%#x", ace.Header.AceType, ace.Mask)
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(user) {
		return fmt.Errorf("private DACL does not belong to the current user")
	}
	return nil
}

func aceCount(dacl *windows.ACL) uint16 {
	if dacl == nil {
		return 0
	}
	return dacl.AceCount
}

func atomicCurrentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close() //nolint:errcheck // token is only used to copy the SID below
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}
