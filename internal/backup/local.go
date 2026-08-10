package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocalBlobStore persists immutable blobs below Root. Files are created in a
// private temporary file, synced, then hard-linked into place so readers never
// observe a partial object and a racing writer cannot overwrite an object.
type LocalBlobStore struct {
	root string
}

// NewLocalBlobStore creates (when necessary) a private store root. The root
// itself must not be a symlink/reparse point; callers should choose a dedicated
// backup directory rather than a live data directory.
func NewLocalBlobStore(root string) (*LocalBlobStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("backup local store root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("backup local store resolve root: %w", err)
	}
	// MkdirAll follows existing links in parent components. Build the directory
	// chain one component at a time instead, so a planted symlink/reparse point
	// anywhere between the volume root and the store cannot redirect blobs.
	if err := ensureTrustedLocalDirectoryChain(abs, true); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("backup local store inspect root: %w", err)
	}
	if isBackupReparsePoint(abs, info) || !info.IsDir() {
		return nil, fmt.Errorf("%w: local store root is not a real directory", ErrInvalidKey)
	}
	return &LocalBlobStore{root: abs}, nil
}

// Root returns the absolute store root.
func (s *LocalBlobStore) Root() string { return s.root }

func (s *LocalBlobStore) PutIfAbsent(ctx context.Context, key string, data []byte, opts PutOptions) (BlobInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, false, err
	}
	if err := validateBlobKey(key); err != nil {
		return BlobInfo{}, false, err
	}
	checksum, err := validatePut(data, opts)
	if err != nil {
		return BlobInfo{}, false, err
	}
	target, err := s.ensureParent(key)
	if err != nil {
		return BlobInfo{}, false, err
	}

	if existing, found, err := s.readExisting(target, key); err != nil {
		return BlobInfo{}, false, err
	} else if found {
		if !bytes.Equal(existing, data) {
			return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
		}
		return BlobInfo{Key: key, Size: int64(len(existing)), SHA256: checksum}, false, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "carbon-backup-*.tmp")
	if err != nil {
		return BlobInfo{}, false, fmt.Errorf("backup local store create temporary object: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return BlobInfo{}, false, fmt.Errorf("backup local store protect temporary object: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return BlobInfo{}, false, fmt.Errorf("backup local store write temporary object: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return BlobInfo{}, false, fmt.Errorf("backup local store sync temporary object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return BlobInfo{}, false, fmt.Errorf("backup local store close temporary object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, false, err
	}

	// Link is create-only on both NTFS and POSIX filesystems. Unlike Rename, it
	// cannot replace an object created by another process between our stat and
	// publish steps.
	if err := os.Link(tmpName, target); err != nil {
		if existing, found, readErr := s.readExisting(target, key); readErr != nil {
			return BlobInfo{}, false, readErr
		} else if found {
			if !bytes.Equal(existing, data) {
				return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
			}
			return BlobInfo{Key: key, Size: int64(len(existing)), SHA256: checksum}, false, nil
		}
		return BlobInfo{}, false, fmt.Errorf("backup local store atomically publish object: %w", err)
	}
	return BlobInfo{Key: key, Size: int64(len(data)), SHA256: checksum}, true, nil
}

func (s *LocalBlobStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	parentKey := filepath.ToSlash(filepath.Dir(key))
	if parentKey == "." {
		parentKey = ""
	}
	if _, exists, err := s.realDirectory(parentKey); err != nil {
		return nil, BlobInfo{}, err
	} else if !exists {
		return nil, BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	target, err := s.pathFor(key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	data, found, err := s.readExisting(target, key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	if !found {
		return nil, BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return data, blobInfo(key, data), nil
}

func (s *LocalBlobStore) Stat(ctx context.Context, key string) (BlobInfo, error) {
	_, info, err := s.Get(ctx, key)
	return info, err
}

// Delete removes one local immutable object. It is intentionally not part of
// BlobStore: remote stores are append-only from Carbon's point of view, and
// retention is a local-only policy. Callers must first prove that the object is
// no longer reachable from every retained manifest.
func (s *LocalBlobStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateBlobKey(key); err != nil {
		return err
	}
	parentKey := filepath.ToSlash(filepath.Dir(key))
	if parentKey == "." {
		parentKey = ""
	}
	if _, exists, err := s.realDirectory(parentKey); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	target, err := s.pathFor(key)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("backup local store inspect deletion target: %w", err)
	}
	if isBackupReparsePoint(target, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: local deletion target is not a regular file", ErrInvalidKey)
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("backup local store remove immutable object: %w", err)
	}
	return nil
}

func (s *LocalBlobStore) List(ctx context.Context, prefix string) ([]BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBlobPrefix(prefix); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	base, exists, err := s.realDirectory(trimmed)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []BlobInfo{}, nil
	}
	infos := make([]BlobInfo, 0)
	err = filepath.WalkDir(base, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if isBackupReparsePoint(filename, entryInfo) {
			return fmt.Errorf("%w: local listed object is a symlink", ErrInvalidKey)
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.root, filename)
		if err != nil {
			return fmt.Errorf("%w: local listed path escapes root", ErrInvalidKey)
		}
		key := filepath.ToSlash(rel)
		if err := validateBlobKey(key); err != nil {
			return err
		}
		data, found, err := s.readExisting(filename, key)
		if err != nil {
			return err
		}
		if found {
			infos = append(infos, blobInfo(key, data))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("backup local store list: %w", err)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	return infos, nil
}

func (s *LocalBlobStore) pathFor(key string) (string, error) {
	if err := validateBlobKey(key); err != nil {
		return "", err
	}
	target := filepath.Join(s.root, filepath.FromSlash(key))
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: path escapes local store", ErrInvalidKey)
	}
	return target, nil
}

// realDirectory walks existing path components with Lstat so a link placed
// below the store root cannot redirect reads or listings outside it.
func (s *LocalBlobStore) realDirectory(key string) (string, bool, error) {
	if key != "" {
		if err := validateBlobKey(key); err != nil {
			return "", false, err
		}
	}
	if err := ensureTrustedLocalDirectoryChain(s.root, false); err != nil {
		return "", false, err
	}
	rootInfo, err := os.Lstat(s.root)
	if err != nil {
		return "", false, fmt.Errorf("backup local store inspect root: %w", err)
	}
	if isBackupReparsePoint(s.root, rootInfo) || !rootInfo.IsDir() {
		return "", false, fmt.Errorf("%w: local store root is not a real directory", ErrInvalidKey)
	}
	current := s.root
	if key == "" {
		return current, true, nil
	}
	for _, part := range strings.Split(key, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return current, false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("backup local store inspect object directory: %w", err)
		}
		if isBackupReparsePoint(current, info) || !info.IsDir() {
			return "", false, fmt.Errorf("%w: local object directory is not a real directory", ErrInvalidKey)
		}
	}
	return current, true, nil
}

// ensureParent creates one path component at a time and rejects pre-existing
// links, preventing a locally planted symlink from redirecting an object write.
func (s *LocalBlobStore) ensureParent(key string) (string, error) {
	target, err := s.pathFor(key)
	if err != nil {
		return "", err
	}
	if err := ensureTrustedLocalDirectoryChain(s.root, false); err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(s.root)
	if err != nil {
		return "", fmt.Errorf("backup local store inspect root: %w", err)
	}
	if isBackupReparsePoint(s.root, rootInfo) || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: local store root is not a real directory", ErrInvalidKey)
	}
	current := s.root
	parts := strings.Split(filepath.ToSlash(filepath.Dir(key)), "/")
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("backup local store create object directory: %w", err)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return "", fmt.Errorf("backup local store inspect object directory: %w", statErr)
		}
		if isBackupReparsePoint(current, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: local object directory is not a real directory", ErrInvalidKey)
		}
	}
	return target, nil
}

func (s *LocalBlobStore) readExisting(target, key string) ([]byte, bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("backup local store inspect object: %w", err)
	}
	if isBackupReparsePoint(target, info) || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: local object is not a regular file", ErrInvalidKey)
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, false, fmt.Errorf("backup local store open object: %w", err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("backup local store read object: %w", readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("backup local store close object: %w", closeErr)
	}
	return data, true, nil
}

// ensureTrustedLocalDirectoryChain checks every existing parent rather than
// merely Lstat'ing the final store directory. This matters when, for example,
// <trusted>/backups is a symlink while <trusted>/backups/local is a real
// directory reached through it. create is used only while initializing a new
// dedicated store root; normal operations never create outside that root.
func ensureTrustedLocalDirectoryChain(directory string, create bool) error {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("backup local store resolve directory chain: %w", err)
	}
	volume := filepath.VolumeName(abs)
	base := string(filepath.Separator)
	if volume != "" {
		base = volume + string(filepath.Separator)
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: local store directory chain escapes its volume root", ErrInvalidKey)
	}

	current := base
	if err := inspectTrustedLocalDirectory(current); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: malformed local store directory chain", ErrInvalidKey)
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if !create {
				return fmt.Errorf("%w: local store directory disappeared", ErrInvalidKey)
			}
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("backup local store create root: %w", err)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return fmt.Errorf("backup local store inspect directory chain: %w", statErr)
		}
		if isBackupReparsePoint(current, info) || !info.IsDir() {
			return fmt.Errorf("%w: local store directory chain contains a symlink or reparse point", ErrInvalidKey)
		}
	}
	return nil
}

func inspectTrustedLocalDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("backup local store inspect directory chain: %w", err)
	}
	if isBackupReparsePoint(directory, info) || !info.IsDir() {
		return fmt.Errorf("%w: local store directory chain is not a real directory", ErrInvalidKey)
	}
	return nil
}
