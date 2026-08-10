//go:build windows

package store

import (
	"strings"

	"golang.org/x/sys/windows"
)

// atomicReplace replaces an existing final entry without first deleting it. Using
// MOVEFILE_WRITE_THROUGH asks Windows to complete the move on disk before returning; if
// a reader/AV keeps the destination open, this returns an error and leaves the old file
// in place rather than creating a delete-then-rename data-loss window.
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

// atomicWindowsPath mirrors the relevant part of os.Rename's long-path handling before
// calling MoveFileEx directly. Keeping that support matters for deeply nested portable
// data homes, while the extended prefix remains a path representation change only.
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
