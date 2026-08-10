//go:build windows

package check

import (
	"os"
	"path/filepath"
)

// defaultShellFallback finds a Git Bash shell installed in a conventional Windows
// location. It is called only for the implicit `sh` default after PATH lookup fails.
func defaultShellFallback() string {
	return firstExistingFile(gitBashShellCandidates())
}

func gitBashShellCandidates() []string {
	return gitBashShellCandidatesFor(
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LocalAppData"),
		os.Getenv("UserProfile"),
	)
}

func gitBashShellCandidatesFor(programFiles, programFilesX86, localAppData, userProfile string) []string {
	candidates := []string{`C:\Program Files\Git\bin\sh.exe`}
	add := func(base string, elems ...string) {
		if base != "" {
			candidates = append(candidates, filepath.Join(append([]string{base}, elems...)...))
		}
	}
	add(programFiles, "Git", "bin", "sh.exe")
	add(programFiles, "Git", "usr", "bin", "sh.exe")
	add(programFilesX86, "Git", "bin", "sh.exe")
	add(localAppData, "Programs", "Git", "bin", "sh.exe")
	add(localAppData, "Git", "bin", "sh.exe")
	add(userProfile, "scoop", "apps", "git", "current", "bin", "sh.exe")
	add(userProfile, "scoop", "apps", "git", "current", "usr", "bin", "sh.exe")
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, path := range candidates {
		if path == "" {
			continue
		}
		key := filepath.Clean(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	return out
}

func firstExistingFile(paths []string) string {
	for _, path := range paths {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path
		}
	}
	return ""
}
