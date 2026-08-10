//go:build !windows

package home

func validateRootInput(_ string) error     { return nil }
func validateCanonicalRoot(_ string) error { return nil }
func validLocalStoredPath(_ string) bool   { return true }
