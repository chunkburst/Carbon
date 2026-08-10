package connect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// trustedConfigPath resolves path from a trusted root without following any
// config-directory link. The root itself is an explicit selection by the caller and
// may be a symlinked project path; every component below it must be a real directory.
// Final config and backup entries are deliberately left to atomic rename: replacing a
// symlink directory entry does not dereference it, which preserves the prior safety
// behavior for a hostile final file while preventing parent-directory escapes.
func trustedConfigPath(trustedRoot, path string, createParents bool) (string, error) {
	if strings.TrimSpace(trustedRoot) == "" {
		return "", fmt.Errorf("%w: missing trusted config root", ErrUnsafeConfigPath)
	}
	rootAbs, err := filepath.Abs(trustedRoot)
	if err != nil {
		return "", fmt.Errorf("resolve trusted config root %q: %w", trustedRoot, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: trusted config root %s", os.ErrNotExist, rootAbs)
		}
		return "", fmt.Errorf("%w: resolve trusted config root %s: %v", ErrUnsafeConfigPath, rootAbs, err)
	}
	rootInfo, err := os.Lstat(rootReal)
	if err != nil {
		return "", fmt.Errorf("%w: inspect trusted config root %s: %v", ErrUnsafeConfigPath, rootReal, err)
	}
	if isConnectReparsePoint(rootReal, rootInfo) || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: trusted config root %s is not a real directory", ErrUnsafeConfigPath, rootAbs)
	}

	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve agent config path %q: %w", path, err)
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: agent config path %s escapes trusted root %s", ErrUnsafeConfigPath, pathAbs, rootAbs)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", fmt.Errorf("%w: agent config path must name a file", ErrUnsafeConfigPath)
	}

	parent, leaf := filepath.Dir(rel), filepath.Base(rel)
	if leaf == "." || leaf == ".." || leaf == "" {
		return "", fmt.Errorf("%w: invalid agent config file name %q", ErrUnsafeConfigPath, leaf)
	}
	current := filepath.Clean(rootReal)
	if parent == "." {
		return filepath.Join(current, leaf), nil
	}
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("%w: invalid agent config parent %q", ErrUnsafeConfigPath, component)
		}
		next := filepath.Join(current, component)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			if !createParents {
				return "", fmt.Errorf("%w: agent config parent %s", os.ErrNotExist, next)
			}
			if err := os.Mkdir(next, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create agent config parent %s: %w", next, err)
			}
			// Re-inspect after creation (including the raced ErrExist case) before using it.
			info, err = os.Lstat(next)
		}
		if err != nil {
			return "", fmt.Errorf("%w: inspect agent config parent %s: %v", ErrUnsafeConfigPath, next, err)
		}
		if isConnectReparsePoint(next, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: refusing symlink, reparse point, or non-directory config parent %s", ErrUnsafeConfigPath, next)
		}
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil || !connectPathWithin(rootReal, resolved) {
			return "", fmt.Errorf("%w: agent config parent %s escapes trusted root", ErrUnsafeConfigPath, next)
		}
		current = filepath.Clean(resolved)
	}
	return filepath.Join(current, leaf), nil
}

// connectPathWithin reports whether path is root itself or a descendant of root. Both
// inputs are cleaned before Rel so platform-specific volume and case rules stay with
// filepath rather than being reconstructed with string prefix checks.
func connectPathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
