package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ReadData reads a small engine-managed metadata file below .carbon/<dir>. It is intended
// for views/templates and deliberately keeps the same symlink protections as task files.
func (s *Store) ReadData(dir, name string) ([]byte, error) {
	path, err := s.dataFilePath(dir, name, false, true, false)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// ListData lists regular file names in a managed metadata directory. A missing directory
// is treated as an empty collection, which keeps old repositories migration-free.
func (s *Store) ListData(dir string) ([]string, error) {
	path, err := s.dataDir(dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := validateDataName(entry.Name()); err != nil {
			return nil, err
		}
		file, err := s.managedFile(path, entry.Name(), true, false)
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(file)
		if err != nil {
			return nil, err
		}
		if fi.Mode().IsRegular() {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadData reads a metadata file while holding the store transaction lock.
func (tx *WriteTx) ReadData(dir, name string) ([]byte, error) { return tx.store.ReadData(dir, name) }

// ListData lists managed metadata while the caller already holds Store.Write's
// repository lock. It is intentionally the transaction counterpart of
// Store.ListData: small durable sidecars that use one record per mutation can
// enumerate a consistent collection without opening a nested transaction.
func (tx *WriteTx) ListData(dir string) ([]string, error) { return tx.store.ListData(dir) }

// WriteData atomically writes a metadata file inside the current store transaction.
func (tx *WriteTx) WriteData(dir, name string, data []byte) error {
	path, err := tx.store.dataFilePath(dir, name, true, false, true)
	if err != nil {
		return err
	}
	return tx.store.writeAtomic(path, data)
}

// DeleteData removes a metadata file inside the current transaction. Missing files are a
// no-op so destructive collection maintenance (empty/GC) is retry-safe.
func (tx *WriteTx) DeleteData(dir, name string) error {
	path, err := tx.store.dataFilePath(dir, name, false, true, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) dataDir(name string, create bool) (string, error) {
	if err := validateDataComponent(name); err != nil {
		return "", err
	}
	return s.managedDir(create, carbonStoreDir, name)
}

func (s *Store) dataFilePath(dir, name string, createDir, requireExisting, rejectSymlink bool) (string, error) {
	if err := validateDataName(name); err != nil {
		return "", err
	}
	path, err := s.dataDir(dir, createDir)
	if err != nil {
		return "", err
	}
	return s.managedFile(path, name, requireExisting, rejectSymlink)
}

func validateDataComponent(name string) error {
	if name == "" || filepath.Base(name) != name || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: %q", ErrInvalidDataPath, name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: %q", ErrInvalidDataPath, name)
	}
	return nil
}

func validateDataName(name string) error {
	if err := validateDataComponent(name); err == nil {
		return nil
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" || filepath.Base(name) != name || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: %q", ErrInvalidDataPath, name)
	}
	if err := validateDataComponent(base); err != nil {
		return err
	}
	ext := filepath.Ext(name)
	if ext == "" || len(ext) > 8 {
		return fmt.Errorf("%w: %q", ErrInvalidDataPath, name)
	}
	for _, r := range ext[1:] {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return fmt.Errorf("%w: %q", ErrInvalidDataPath, name)
	}
	return nil
}
