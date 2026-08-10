//go:build !windows

package check

// defaultShellFallback is intentionally empty outside Windows: POSIX systems use the
// normal PATH lookup for their default shell.
func defaultShellFallback() string { return "" }
