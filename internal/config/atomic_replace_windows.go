//go:build windows

package config

import (
	"strings"

	"golang.org/x/sys/windows"
)

// atomicReplace uses replacement in one Windows operation. It intentionally does not
// remove an old file first: if a competing reader prevents replacement, the old config
// remains available rather than leaving a missing-file interval.
func atomicReplace(from, to string) error {
	fromPath, err := atomicWindowsPath(from)
	if err != nil {
		return err
	}
	toPath, err := atomicWindowsPath(to)
	if err != nil {
		return err
	}
	fromPtr, err := windows.UTF16PtrFromString(fromPath)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(toPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func atomicWindowsPath(path string) (string, error) {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) {
		return path, nil
	}
	full, err := windows.FullPath(path)
	if err != nil {
		return "", err
	}
	units, err := windows.UTF16FromString(full)
	if err != nil || len(units) < 248 {
		return full, err
	}
	if strings.HasPrefix(full, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(full, `\\`), nil
	}
	return `\\?\` + full, nil
}
