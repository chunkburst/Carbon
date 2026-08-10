package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// CarbonDirName is the only directory new Carbon code reads or writes.
	CarbonDirName = ".carbon"
	// LegacyCairnDirName is accepted only as an import source.
	LegacyCairnDirName = ".cairn"

	migrationLockFilename    = ".carbon-migration.lock"
	migrationStagePrefix     = "carbon-migrate-"
	migrationReceiptFilename = "carbon-migration-receipt.json"
	migrationStageSuffix     = ".receipt.json"
	migrationSchemaVersion   = 1

	migrationLockTimeout = 5 * time.Second
	migrationLockRetry   = 20 * time.Millisecond
	// A dead process can leave an O_EXCL lock behind. A live migration normally lasts
	// seconds, so this conservative age makes recovery possible without stealing a
	// currently-running operation.
	migrationStaleLockAge = time.Hour
)

var (
	// ErrUnsafeLegacyMigration means the legacy tree cannot be copied without following
	// a symlink, reparse point, special file, or escaping path.
	ErrUnsafeLegacyMigration = errors.New("repo: unsafe legacy Carbon migration")
	// ErrMigrationLocked means another Carbon migration did not finish before the
	// bounded wait elapsed.
	ErrMigrationLocked = errors.New("repo: Carbon migration is locked")
)

// MigrationReceipt records a verified, one-time legacy import. It intentionally stores
// only relative names and content facts, never user-controlled absolute paths.
type MigrationReceipt struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Digest      string `json:"digest"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
	Bytes       int64  `json:"bytes"`
	CompletedAt string `json:"completedAt"`
}

type treeSnapshot struct {
	Digest      string
	Files       int
	Directories int
	Bytes       int64
}

// EnsureCarbonStore imports a legacy .cairn tree into .carbon when, and only when, a
// canonical tree does not already exist. It never deletes or mutates the legacy tree.
// Calling it repeatedly is safe: .carbon always wins if both trees exist.
func EnsureCarbonStore(root string) error {
	resolvedRoot, err := repositoryRoot(root)
	if err != nil {
		return err
	}
	carbon, carbonExists, err := ownedDirectDirectory(resolvedRoot, CarbonDirName)
	if err != nil {
		return err
	}
	if carbonExists {
		return recoverMigrationReceipt(resolvedRoot, carbon)
	}
	legacy, legacyExists, err := ownedDirectDirectory(resolvedRoot, LegacyCairnDirName)
	if err != nil {
		return err
	}
	if !legacyExists {
		return nil
	}

	release, err := acquireMigrationLock(resolvedRoot)
	if err != nil {
		return err
	}
	defer release()

	// A competing process may have completed the migration while this caller waited.
	if carbon, carbonExists, err = ownedDirectDirectory(resolvedRoot, CarbonDirName); err != nil {
		return err
	} else if carbonExists {
		return recoverMigrationReceipt(resolvedRoot, carbon)
	}
	if legacy, legacyExists, err = ownedDirectDirectory(resolvedRoot, LegacyCairnDirName); err != nil {
		return err
	} else if !legacyExists {
		return nil
	}

	if recovered, err := recoverInterruptedMigration(resolvedRoot, legacy); err != nil {
		return err
	} else if recovered {
		return nil
	}
	return importLegacyTree(resolvedRoot, legacy)
}

// MigrationReceiptPath returns the root-level receipt path. The receipt deliberately
// lives outside .carbon so an old store file with the same basename remains byte-for-byte
// preserved during the directory import.
func MigrationReceiptPath(root string) (string, error) {
	resolved, err := repositoryRoot(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, migrationReceiptFilename), nil
}

// ReadMigrationReceipt returns the verified receipt of a legacy import when one exists.
// It is primarily useful to recovery tooling; normal callers only need EnsureCarbonStore.
func ReadMigrationReceipt(root string) (MigrationReceipt, bool, error) {
	path, err := MigrationReceiptPath(root)
	if err != nil {
		return MigrationReceipt{}, false, err
	}
	return readReceiptFile(path)
}

func ownedDirectDirectory(root, name string) (string, bool, error) {
	if filepath.Base(name) != name || name == "." || name == ".." || name == "" {
		return "", false, fmt.Errorf("%w: invalid managed directory name %q", ErrUnsafeLegacyMigration, name)
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if isRepoReparsePoint(path, info) || !info.IsDir() {
		if name == CarbonDirName {
			return "", false, fmt.Errorf("%w: %w: refusing non-directory or reparse point %s", ErrUnsafeLegacyMigration, ErrCarbonPathOutsideRoot, path)
		}
		return "", false, fmt.Errorf("%w: refusing non-directory or reparse point %s", ErrUnsafeLegacyMigration, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithin(root, resolved) || filepath.Clean(resolved) != filepath.Clean(path) {
		if name == CarbonDirName {
			return "", false, fmt.Errorf("%w: %w: managed directory escapes root: %s", ErrUnsafeLegacyMigration, ErrCarbonPathOutsideRoot, path)
		}
		return "", false, fmt.Errorf("%w: managed directory escapes root: %s", ErrUnsafeLegacyMigration, path)
	}
	return filepath.Clean(path), true, nil
}

func importLegacyTree(root, legacy string) error {
	source, err := snapshotTree(legacy)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, migrationStagePrefix)
	if err != nil {
		return fmt.Errorf("repo: create Carbon migration staging directory: %w", err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil || isRepoReparsePoint(stage, stageInfo) || !stageInfo.IsDir() {
		return fmt.Errorf("%w: invalid migration staging directory", ErrUnsafeLegacyMigration)
	}
	if err := copyTree(legacy, stage); err != nil {
		return err
	}
	target, err := snapshotTree(stage)
	if err != nil {
		return err
	}
	if !sameSnapshot(source, target) {
		return fmt.Errorf("%w: copied tree digest mismatch", ErrUnsafeLegacyMigration)
	}
	// Re-read the source after copying so a concurrent legacy writer cannot publish a
	// mixed snapshot under the canonical name.
	if after, err := snapshotTree(legacy); err != nil {
		return err
	} else if !sameSnapshot(source, after) {
		return fmt.Errorf("%w: legacy tree changed during migration", ErrUnsafeLegacyMigration)
	}
	receipt := receiptFor(source)
	if err := writeStageReceipt(stage, receipt); err != nil {
		return err
	}
	if _, exists, err := ownedDirectDirectory(root, CarbonDirName); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := os.Rename(stage, filepath.Join(root, CarbonDirName)); err != nil {
		return fmt.Errorf("repo: atomically publish Carbon migration: %w", err)
	}
	if err := publishMigrationReceipt(root, stage, receipt); err != nil {
		return err
	}
	syncDirectory(root)
	return nil
}

func recoverInterruptedMigration(root, legacy string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), migrationStagePrefix) || !entry.IsDir() {
			continue
		}
		stage := filepath.Join(root, entry.Name())
		info, err := os.Lstat(stage)
		if err != nil || isRepoReparsePoint(stage, info) || !info.IsDir() {
			return false, fmt.Errorf("%w: invalid interrupted migration staging directory", ErrUnsafeLegacyMigration)
		}
		receipt, exists, err := readStageReceipt(stage)
		if err != nil {
			return false, err
		}
		if !exists {
			// An interruption before verification never becomes canonical. Leaving the
			// private staging directory in place is safer than recursively deleting a
			// path after a crash or hostile filesystem change.
			continue
		}
		source, err := snapshotTree(legacy)
		if err != nil {
			return false, err
		}
		target, err := snapshotTree(stage)
		if err != nil {
			return false, err
		}
		if !receiptMatches(receipt, source) || !sameSnapshot(source, target) {
			return false, fmt.Errorf("%w: interrupted migration no longer matches legacy tree", ErrUnsafeLegacyMigration)
		}
		if err := os.Rename(stage, filepath.Join(root, CarbonDirName)); err != nil {
			return false, fmt.Errorf("repo: recover Carbon migration: %w", err)
		}
		if err := publishMigrationReceipt(root, stage, receipt); err != nil {
			return false, err
		}
		syncDirectory(root)
		return true, nil
	}
	return false, nil
}

func recoverMigrationReceipt(root, carbon string) error {
	path := filepath.Join(root, migrationReceiptFilename)
	if _, exists, err := readReceiptFile(path); err != nil {
		return err
	} else if exists {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), migrationStagePrefix) || !strings.HasSuffix(entry.Name(), migrationStageSuffix) || entry.IsDir() {
			continue
		}
		stageBase := strings.TrimSuffix(entry.Name(), migrationStageSuffix)
		receipt, exists, err := readReceiptFile(filepath.Join(root, entry.Name()))
		if err != nil || !exists {
			return err
		}
		if _, err := os.Lstat(filepath.Join(root, stageBase)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// The directory was already atomically renamed. A matching canonical tree is
		// enough to finish publication of the durable receipt.
		target, err := snapshotTree(carbon)
		if err != nil {
			return err
		}
		if receiptMatches(receipt, target) {
			return publishMigrationReceipt(root, filepath.Join(root, stageBase), receipt)
		}
	}
	return nil
}

func receiptFor(snapshot treeSnapshot) MigrationReceipt {
	return MigrationReceipt{
		Version: migrationSchemaVersion, Source: LegacyCairnDirName, Digest: snapshot.Digest,
		Files: snapshot.Files, Directories: snapshot.Directories, Bytes: snapshot.Bytes,
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func receiptMatches(receipt MigrationReceipt, snapshot treeSnapshot) bool {
	return receipt.Version == migrationSchemaVersion && receipt.Source == LegacyCairnDirName &&
		receipt.Digest == snapshot.Digest && receipt.Files == snapshot.Files &&
		receipt.Directories == snapshot.Directories && receipt.Bytes == snapshot.Bytes
}

func writeStageReceipt(stage string, receipt MigrationReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePrivateFile(stage+migrationStageSuffix, data)
}

func readStageReceipt(stage string) (MigrationReceipt, bool, error) {
	return readReceiptFile(stage + migrationStageSuffix)
}

func readReceiptFile(path string) (MigrationReceipt, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return MigrationReceipt{}, false, nil
	}
	if err != nil {
		return MigrationReceipt{}, false, err
	}
	if isRepoReparsePoint(path, info) || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return MigrationReceipt{}, false, fmt.Errorf("%w: invalid migration receipt", ErrUnsafeLegacyMigration)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return MigrationReceipt{}, false, err
	}
	var receipt MigrationReceipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Version != migrationSchemaVersion || receipt.Source != LegacyCairnDirName || receipt.Digest == "" {
		return MigrationReceipt{}, false, fmt.Errorf("%w: invalid migration receipt", ErrUnsafeLegacyMigration)
	}
	return receipt, true, nil
}

func publishMigrationReceipt(root, stage string, receipt MigrationReceipt) error {
	final := filepath.Join(root, migrationReceiptFilename)
	if info, err := os.Lstat(final); err == nil {
		if isRepoReparsePoint(final, info) || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: invalid migration receipt destination", ErrUnsafeLegacyMigration)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sidecar := stage + migrationStageSuffix
	if _, exists, err := readReceiptFile(sidecar); err != nil || !exists {
		if err != nil {
			return err
		}
		data, marshalErr := json.Marshal(receipt)
		if marshalErr != nil {
			return marshalErr
		}
		data = append(data, '\n')
		return writePrivateFile(final, data)
	}
	if err := os.Rename(sidecar, final); err != nil {
		return fmt.Errorf("repo: publish migration receipt: %w", err)
	}
	return nil
}

func writePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func snapshotTree(root string) (treeSnapshot, error) {
	h := sha256.New()
	var snapshot treeSnapshot
	var walk func(string, string) error
	walk = func(dir, rel string) error {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if isRepoReparsePoint(dir, info) || !info.IsDir() {
			return fmt.Errorf("%w: refusing non-directory or reparse point %s", ErrUnsafeLegacyMigration, dir)
		}
		// The source root (.cairn) and migration stage are different container
		// directories: MkdirTemp intentionally creates the latter private (0700).
		// Their own mode is not migrated state, so hash only relative descendants.
		// The root is still lstat-validated above before any traversal begins.
		if rel != "" {
			snapshot.Directories++
			writeDigestRecord(h, "d", rel, fmt.Sprintf("%o", info.Mode().Perm()))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if filepath.Base(name) != name || name == "." || name == ".." {
				return fmt.Errorf("%w: invalid legacy tree entry %q", ErrUnsafeLegacyMigration, name)
			}
			if rel == "" && isEphemeralLegacyEntry(name) {
				continue
			}
			path := filepath.Join(dir, name)
			childRel := name
			if rel != "" {
				childRel = filepath.Join(rel, name)
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if isRepoReparsePoint(path, info) {
				return fmt.Errorf("%w: refusing reparse point %s", ErrUnsafeLegacyMigration, path)
			}
			switch {
			case info.IsDir():
				if err := walk(path, childRel); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if err := hashRegularFile(h, path, childRel, info, &snapshot); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: refusing special file %s", ErrUnsafeLegacyMigration, path)
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return treeSnapshot{}, err
	}
	snapshot.Digest = hex.EncodeToString(h.Sum(nil))
	return snapshot, nil
}

func hashRegularFile(h io.Writer, path, rel string, info os.FileInfo, snapshot *treeSnapshot) error {
	writeDigestRecord(h, "f", rel, fmt.Sprintf("%o", info.Mode().Perm()), fmt.Sprintf("%d", info.Size()))
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fmt.Errorf("%w: legacy file changed while reading %s", ErrUnsafeLegacyMigration, path)
	}
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	snapshot.Files++
	snapshot.Bytes += info.Size()
	return nil
}

func copyTree(source, target string) error {
	return copyTreeAt(source, target, true)
}

func copyTreeAt(source, target string, isLegacyRoot bool) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Base(name) != name || name == "." || name == ".." {
			return fmt.Errorf("%w: invalid legacy tree entry %q", ErrUnsafeLegacyMigration, name)
		}
		if isLegacyRoot && isEphemeralLegacyEntry(name) {
			continue
		}
		src := filepath.Join(source, name)
		dst := filepath.Join(target, name)
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if isRepoReparsePoint(src, info) {
			return fmt.Errorf("%w: refusing reparse point %s", ErrUnsafeLegacyMigration, src)
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
				return err
			}
			if err := copyTreeAt(src, dst, false); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyRegularFile(src, dst, info); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: refusing special file %s", ErrUnsafeLegacyMigration, src)
		}
	}
	return nil
}

// live contains current-process lease heartbeats and write.lock is an advisory lock;
// neither is durable task state. Importing either could resurrect a stale worker or
// make a newly opened Carbon store look locked, so migration intentionally recreates
// these paths on demand and excludes them from the verified snapshot.
func isEphemeralLegacyEntry(name string) bool {
	return name == "live" || name == "write.lock"
}

func copyRegularFile(source, target string, expected os.FileInfo) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return fmt.Errorf("%w: legacy file changed while copying %s", ErrUnsafeLegacyMigration, source)
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, expected.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeDigestRecord(h io.Writer, fields ...string) {
	for _, field := range fields {
		_, _ = io.WriteString(h, field)
		_, _ = io.WriteString(h, "\x00")
	}
}

func sameSnapshot(a, b treeSnapshot) bool {
	return a.Digest == b.Digest && a.Files == b.Files && a.Directories == b.Directories && a.Bytes == b.Bytes
}

func acquireMigrationLock(root string) (func(), error) {
	path := filepath.Join(root, migrationLockFilename)
	deadline := time.Now().Add(migrationLockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("repo: create Carbon migration lock: %w", err)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			continue
		}
		if isRepoReparsePoint(path, info) || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: invalid migration lock", ErrUnsafeLegacyMigration)
		}
		if time.Since(info.ModTime()) > migrationStaleLockAge {
			if err := os.Remove(path); err == nil {
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, ErrMigrationLocked
		}
		time.Sleep(migrationLockRetry)
	}
}

func syncDirectory(path string) {
	if dir, err := os.Open(path); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
}
