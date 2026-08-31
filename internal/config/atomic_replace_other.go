//go:build !windows

package config

import "os"

func atomicReplace(from, to string) error {
	return os.Rename(from, to)
}
