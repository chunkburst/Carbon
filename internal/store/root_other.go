//go:build !windows

package store

// POSIX Store roots retain their established canonical-symlink behavior. Managed
// children are still checked component-by-component by managedDir/writeAtomic.
func validateStoreRootInput(_ string) error { return nil }
func validateStoreRootPath(_ string) error  { return nil }
