//go:build windows

package store

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ALL_ACCESS is the file-object mapping Windows persists when GENERIC_ALL is
// supplied through SetEntriesInAcl. x/sys exposes the component constants but not this
// aggregate.
const atomicFileAllAccess windows.ACCESS_MASK = 0x001F01FF

// secureAtomicTempFile replaces inherited DACLs with one protected ACE for the current
// process user before content is written. os.File.Chmod(0600) does not tighten Windows
// DACLs, so it is intentionally not used as a security claim here. Administrators can
// still take ownership by OS design; this prevents inherited Users/Everyone access. It
// operates on the already-opened temp handle, not the pathname, so a temp-name swap
// cannot redirect a DACL update to another file.
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

// openAtomicSecurityHandle reopens the staged file with WRITE_DAC while retaining a
// no-follow handle. os.CreateTemp's normal read/write handle does not include WRITE_DAC
// on Windows, so SetSecurityInfo directly on it can fail with ACCESS_DENIED. Comparing
// stable file IDs prevents a path swap between CreateTemp and this reopen from changing
// ACLs on another object.
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

// validateAtomicTempFile reopens the staged pathname without following a reparse point
// and compares its file ID with the original handle. It prevents a regular-file temp
// name substitution from being published accidentally; a final same-identity rename
// race remains an unavoidable limitation of pathname-based MoveFileEx and is documented
// at the writer call site.
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

// removeAtomicTempFile refuses to unlink an entry when its file ID has changed. The
// final Remove is still pathname-based, so a hostile same-identity directory rename in
// the tiny check/remove interval cannot be eliminated without lower-level handle APIs.
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
		return errorsNewPrivateDACLUserMismatch()
	}
	return nil
}

func aceCount(dacl *windows.ACL) uint16 {
	if dacl == nil {
		return 0
	}
	return dacl.AceCount
}

func errorsNewPrivateDACLUserMismatch() error {
	return fmt.Errorf("private DACL does not belong to the current user")
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
