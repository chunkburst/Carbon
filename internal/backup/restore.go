package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RestoreOptions controls where a verified snapshot is staged. Restore never
// writes into a caller's live directory and never replaces an existing path.
type RestoreOptions struct {
	// StagingDir is an optional exact new directory to create. It must not
	// already exist. When empty, Restore creates a unique directory below
	// TempParent (or ApprovedRoot when TempParent is omitted).
	StagingDir string
	// TempParent is used only when StagingDir is empty. When omitted, the
	// staging directory is created directly below ApprovedRoot.
	TempParent string
	// ApprovedRoot is the required trusted boundary for either staging mode.
	// It and every existing component below it must be a real, non-reparse
	// directory. Restore rejects a staging or temporary parent outside it.
	ApprovedRoot string
}

// RestoreResult identifies the complete staged tree. The caller may inspect it
// and choose how to atomically replace a live target; this package never makes
// that replacement decision.
type RestoreResult struct {
	StagingDir string
	Snapshot   Snapshot
	Manifest   Manifest
}

// Restore verifies every object before creating a staging directory, then
// verifies each object again as it is written. On any failure it removes only
// the staging directory that it just created and leaves all existing paths
// untouched.
func (r *Repository) Restore(ctx context.Context, snapshot Snapshot, options RestoreOptions) (RestoreResult, error) {
	return r.RestoreToStaging(ctx, snapshot, options)
}

// RestoreToStaging is the explicit restore API used by adapters. It has the
// same behavior as Restore and exists to make it clear that no live-path
// replacement occurs in this package.
func (r *Repository) RestoreToStaging(ctx context.Context, snapshot Snapshot, options RestoreOptions) (RestoreResult, error) {
	approvedRoot, err := canonicalTrustedRestoreDirectory(options.ApprovedRoot, "approved restore root")
	if err != nil {
		return RestoreResult{}, err
	}
	options.ApprovedRoot = approvedRoot
	manifest, err := r.Verify(ctx, snapshot)
	if err != nil {
		return RestoreResult{}, err
	}
	staging, err := createStagingDirectory(options)
	if err != nil {
		return RestoreResult{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(staging)
		}
	}()

	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return RestoreResult{}, err
		}
		data, err := r.getChecked(ctx, ObjectKey(file.SHA256), file.SHA256, file.Size)
		if err != nil {
			return RestoreResult{}, fmt.Errorf("backup restore %q: %w", file.Path, err)
		}
		if err := writeStagedFile(staging, approvedRoot, file, data); err != nil {
			return RestoreResult{}, err
		}
	}
	success = true
	return RestoreResult{StagingDir: staging, Snapshot: snapshot, Manifest: manifest}, nil
}

func createStagingDirectory(options RestoreOptions) (string, error) {
	approvedRoot, err := canonicalTrustedRestoreDirectory(options.ApprovedRoot, "approved restore root")
	if err != nil {
		return "", err
	}
	if options.StagingDir == "" {
		parent := strings.TrimSpace(options.TempParent)
		if parent == "" {
			parent = approvedRoot
		}
		parent, err := trustedRestoreDirectory(parent, approvedRoot)
		if err != nil {
			return "", err
		}
		staging, err := os.MkdirTemp(parent, "carbon-restore-")
		if err != nil {
			return "", fmt.Errorf("backup restore create staging directory: %w", err)
		}
		if _, err := trustedRestoreDirectory(staging, approvedRoot); err != nil {
			_ = os.Remove(staging)
			return "", err
		}
		return staging, nil
	}
	staging, err := filepath.Abs(strings.TrimSpace(options.StagingDir))
	if err != nil {
		return "", fmt.Errorf("backup restore resolve staging directory: %w", err)
	}
	if strings.TrimSpace(staging) == "" || filepath.Base(staging) == "." {
		return "", fmt.Errorf("%w: invalid staging directory", ErrUnsafePath)
	}
	if _, err := trustedRestoreDirectory(filepath.Dir(staging), approvedRoot); err != nil {
		return "", err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("backup restore staging directory already exists")
		}
		return "", fmt.Errorf("backup restore create staging directory: %w", err)
	}
	if _, err := trustedRestoreDirectory(staging, approvedRoot); err != nil {
		_ = os.Remove(staging)
		return "", err
	}
	return staging, nil
}

func writeStagedFile(root, approvedRoot string, entry FileEntry, data []byte) error {
	if _, err := trustedRestoreDirectory(root, approvedRoot); err != nil {
		return err
	}
	path, err := cleanRelativePath(entry.Path)
	if err != nil || path != entry.Path {
		return fmt.Errorf("%w: invalid restore path", ErrUnsafePath)
	}
	target, err := safeJoin(root, path)
	if err != nil {
		return err
	}
	if err := ensureStagingParent(root, filepath.Dir(target)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "carbon-restore-file-*.tmp")
	if err != nil {
		return fmt.Errorf("backup restore create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(os.FileMode(entry.Mode)); err != nil {
		tmp.Close()
		return fmt.Errorf("backup restore set file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("backup restore write file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("backup restore sync file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup restore close file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("backup restore publish file: %w", err)
	}
	return nil
}

func safeJoin(root, relative string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: restore path escapes staging directory", ErrUnsafePath)
	}
	return target, nil
}

func ensureStagingParent(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: restore parent escapes staging directory", ErrUnsafePath)
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: invalid restore parent", ErrUnsafePath)
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("backup restore create directory: %w", err)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return fmt.Errorf("backup restore inspect directory: %w", statErr)
		}
		if isBackupReparsePoint(current, info) || !info.IsDir() {
			return fmt.Errorf("%w: restore directory is not a real directory", ErrUnsafePath)
		}
	}
	return nil
}

// trustedRestoreDirectory accepts only a canonical real directory below an
// explicitly approved canonical root. Both paths are walked component by
// component before use, so a symlink or Windows reparse point in either chain
// is rejected rather than followed.
func trustedRestoreDirectory(directory, approvedRoot string) (string, error) {
	approved, err := canonicalTrustedRestoreDirectory(approvedRoot, "approved restore root")
	if err != nil {
		return "", err
	}
	trusted, err := canonicalTrustedRestoreDirectory(directory, "restore directory")
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(approved, trusted)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: restore staging directory is outside approved root", ErrUnsafePath)
	}
	return trusted, nil
}

// canonicalTrustedRestoreDirectory resolves a required existing directory only
// after validating every lexical path component. EvalSymlinks then yields a
// canonical path, which is checked again before it participates in a
// containment decision.
func canonicalTrustedRestoreDirectory(directory, label string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("%w: %s is required", ErrUnsafePath, label)
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("backup restore resolve %s: %w", label, err)
	}
	if err := ensureTrustedLocalDirectoryChain(abs, false); err != nil {
		return "", fmt.Errorf("%w: %s is not a trusted real path", ErrUnsafePath, label)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: cannot canonicalize %s", ErrUnsafePath, label)
	}
	canonical = filepath.Clean(canonical)
	if err := ensureTrustedLocalDirectoryChain(canonical, false); err != nil {
		return "", fmt.Errorf("%w: %s is not a trusted real path", ErrUnsafePath, label)
	}
	return canonical, nil
}
