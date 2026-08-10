//go:build windows

package config

import (
	"errors"
	"testing"
)

func TestConfigAtomicWriteRejectsWindowsRemoteAndDeviceNamespaces(t *testing.T) {
	for _, path := range []string{
		`\\server\share\config.yaml`,
		`//server/share/config.yaml`,
		`\\?\C:\config.yaml`,
		`\\.\PhysicalDrive0`,
		`\??\C:\config.yaml`,
	} {
		if err := Save(path, Default("PROJ")); !errors.Is(err, ErrUnsafeConfigPath) {
			t.Fatalf("Save(%q) = %v, want ErrUnsafeConfigPath", path, err)
		}
	}
}
