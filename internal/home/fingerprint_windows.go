//go:build windows

package home

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// sourceFingerprint uses the Windows volume serial plus directory file index, which is
// preserved by a rename on the same volume and deliberately contains no source pathname.
func sourceFingerprint(root string) (string, error) {
	f, err := os.Open(root)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return "", err
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return fmt.Sprintf("fs:%x:%x", info.VolumeSerialNumber, index), nil
}
