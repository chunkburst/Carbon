//go:build !windows

package config

func validateConfigAtomicPathInput(_ string) error { return nil }
func validateConfigAtomicPathRoot(_ string) error  { return nil }
