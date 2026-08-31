package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	// ErrInvalidSnapshot indicates malformed, unsupported, or corrupt snapshot metadata.
	ErrInvalidSnapshot = errors.New("invalid backup snapshot")
	// ErrUnsafePath indicates a manifest or source path that could escape its root.
	ErrUnsafePath = errors.New("unsafe backup path")
	// ErrUnsupportedEntry indicates a non-regular source filesystem entry.
	ErrUnsupportedEntry = errors.New("unsupported backup filesystem entry")
)

// SchemaVersion is the current canonical Carbon snapshot manifest schema.
const SchemaVersion = "carbon.snapshot/v1"

// DefaultAppVersion is used when an integration does not inject its running app version.
const DefaultAppVersion = "1.1.2"

// FileEntry is one immutable regular file in a snapshot manifest.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// Manifest is canonical JSON and is itself stored as an immutable,
// content-addressed object. SourceID is an integration-provided stable opaque
// ID, not a path that might change over time.
type Manifest struct {
	SchemaVersion string      `json:"schema_version"`
	AppVersion    string      `json:"app_version"`
	CreatedAt     time.Time   `json:"created_at"`
	SourceID      string      `json:"source_id"`
	Files         []FileEntry `json:"files"`
}

// Snapshot points to a manifest by its SHA-256 content ID. ManifestKey is
// redundant convenience metadata and is validated when supplied.
type Snapshot struct {
	ID          string `json:"id"`
	ManifestKey string `json:"manifest_key"`
}

// SnapshotInfo is a manifest-bearing entry returned by Repository.List. List
// validates each manifest but does not fetch all of its file objects; call
// Verify before restoring or otherwise trusting complete snapshot contents.
type SnapshotInfo struct {
	Snapshot Snapshot `json:"snapshot"`
	Manifest Manifest `json:"manifest"`
}

// ExcludeFunc adds caller-specific exclusions. Default safety exclusions are
// always applied first and cannot be disabled by this function.
type ExcludeFunc func(relativePath string, entry fs.DirEntry) bool

// CreateOptions supplies the source directory and its stable identity.
type CreateOptions struct {
	SourceDir  string
	SourceID   string
	AppVersion string
	Exclude    ExcludeFunc
}

// Repository creates, verifies, restores, and replicates snapshots through an
// immutable BlobStore. It is safe for concurrent use when its store is safe.
type Repository struct {
	store      BlobStore
	appVersion string
	now        func() time.Time
}

// NewRepository binds backup operations to a BlobStore. Passing an empty app
// version selects DefaultAppVersion; an integration may override it per Create.
func NewRepository(store BlobStore, appVersion string) (*Repository, error) {
	if store == nil {
		return nil, errors.New("backup repository has nil blob store")
	}
	if strings.TrimSpace(appVersion) == "" {
		appVersion = DefaultAppVersion
	}
	return &Repository{store: store, appVersion: appVersion, now: time.Now}, nil
}

// Store returns the repository's immutable object-store boundary.
func (r *Repository) Store() BlobStore { return r.store }

// Create stores a content-addressed object for each eligible regular file and
// then publishes a canonical immutable manifest. It never traverses symlinks
// and never captures default-excluded locks, temporary files, caches, or likely
// credential material.
func (r *Repository) Create(ctx context.Context, options CreateOptions) (Snapshot, error) {
	snapshot, _, err := r.create(ctx, options, false)
	return snapshot, err
}

// CreateIfChanged creates a manifest only when the durable source content has
// changed since a valid local manifest with the same source and app version.
// It returns created=false when it reuses an existing manifest. File objects
// remain content-addressed in either case. A malformed existing manifest makes
// this operation fail closed rather than allowing a scheduler to silently
// bypass corruption.
func (r *Repository) CreateIfChanged(ctx context.Context, options CreateOptions) (snapshot Snapshot, created bool, err error) {
	return r.create(ctx, options, true)
}

func (r *Repository) create(ctx context.Context, options CreateOptions, reuseUnchanged bool) (Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	if strings.TrimSpace(options.SourceID) == "" {
		return Snapshot{}, false, fmt.Errorf("%w: source ID is required", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(options.SourceDir) == "" {
		return Snapshot{}, false, fmt.Errorf("%w: source directory is required", ErrUnsafePath)
	}
	root, err := filepath.Abs(options.SourceDir)
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("backup snapshot resolve source: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("backup snapshot inspect source: %w", err)
	}
	if isBackupReparsePoint(root, rootInfo) || !rootInfo.IsDir() {
		return Snapshot{}, false, fmt.Errorf("%w: source root is not a real directory", ErrUnsafePath)
	}

	entries := make([]FileEntry, 0)
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("backup snapshot walk source: %w", walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == root {
			return nil
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return fmt.Errorf("%w: cannot derive source-relative path", ErrUnsafePath)
		}
		rel, err = cleanRelativePath(rel)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("backup snapshot inspect source entry: %w", err)
		}
		excluded := defaultExcluded(rel) || (options.Exclude != nil && options.Exclude(rel, entry))
		if isBackupReparsePoint(filename, info) {
			if excluded {
				if entry.IsDir() || info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return fmt.Errorf("%w: reparse point %q", ErrUnsupportedEntry, rel)
		}
		if excluded {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %q", ErrUnsupportedEntry, rel)
		}
		data, err := readStableFile(filename, info)
		if err != nil {
			return err
		}
		checksum := SHA256Hex(data)
		if err := r.putImmutable(ctx, ObjectKey(checksum), data, checksum); err != nil {
			return err
		}
		entries = append(entries, FileEntry{
			Path:   rel,
			SHA256: checksum,
			Size:   int64(len(data)),
			Mode:   uint32(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return Snapshot{}, false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	appVersion := options.AppVersion
	if strings.TrimSpace(appVersion) == "" {
		appVersion = r.appVersion
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		AppVersion:    appVersion,
		CreatedAt:     r.now().UTC(),
		SourceID:      options.SourceID,
		Files:         entries,
	}
	data, err := marshalManifest(manifest)
	if err != nil {
		return Snapshot{}, false, err
	}
	if reuseUnchanged {
		existing, found, err := r.findEquivalentSnapshot(ctx, manifest)
		if err != nil {
			return Snapshot{}, false, err
		}
		if found {
			return existing, false, nil
		}
	}
	id := SHA256Hex(data)
	key := ManifestKey(id)
	if err := r.putImmutable(ctx, key, data, id); err != nil {
		return Snapshot{}, false, err
	}
	return Snapshot{ID: id, ManifestKey: key}, true, nil
}

func (r *Repository) findEquivalentSnapshot(ctx context.Context, candidate Manifest) (Snapshot, bool, error) {
	if _, ok := r.store.(BlobLister); !ok {
		return Snapshot{}, false, nil
	}
	existing, err := r.List(ctx)
	if err != nil {
		return Snapshot{}, false, err
	}
	for _, item := range existing {
		if sameManifestContent(item.Manifest, candidate) {
			return item.Snapshot, true, nil
		}
	}
	return Snapshot{}, false, nil
}

func sameManifestContent(left, right Manifest) bool {
	if left.SchemaVersion != right.SchemaVersion || left.AppVersion != right.AppVersion || left.SourceID != right.SourceID || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index] != right.Files[index] {
			return false
		}
	}
	return true
}

// Verify validates the manifest content ID, canonical schema, every manifest
// entry, and every file object before returning the trusted manifest.
func (r *Repository) Verify(ctx context.Context, snapshot Snapshot) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	manifest, _, err := r.loadManifest(ctx, snapshot)
	if err != nil {
		return Manifest{}, err
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if _, err := r.getChecked(ctx, ObjectKey(file.SHA256), file.SHA256, file.Size); err != nil {
			return Manifest{}, fmt.Errorf("backup verify %q: %w", file.Path, err)
		}
	}
	return manifest, nil
}

// LoadManifest validates and returns a manifest but does not fetch every file
// object. Verify should be used before restore, remote publication, or trust.
func (r *Repository) LoadManifest(ctx context.Context, snapshot Snapshot) (Manifest, error) {
	manifest, _, err := r.loadManifest(ctx, snapshot)
	return manifest, err
}

// List returns recognized immutable manifests in newest-first creation order.
// The underlying store must implement BlobLister. Unknown objects under the
// manifest prefix are ignored, while a recognized but malformed manifest fails
// closed rather than being silently hidden.
func (r *Repository) List(ctx context.Context) ([]SnapshotInfo, error) {
	lister, ok := r.store.(BlobLister)
	if !ok {
		return nil, ErrListingUnsupported
	}
	items, err := lister.List(ctx, "manifests/sha256/")
	if err != nil {
		return nil, err
	}
	results := make([]SnapshotInfo, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := manifestIDFromKey(item.Key)
		if !ok {
			continue
		}
		snapshot := Snapshot{ID: id, ManifestKey: item.Key}
		manifest, err := r.LoadManifest(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		results = append(results, SnapshotInfo{Snapshot: snapshot, Manifest: manifest})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Manifest.CreatedAt.Equal(results[j].Manifest.CreatedAt) {
			return results[i].Snapshot.ID < results[j].Snapshot.ID
		}
		return results[i].Manifest.CreatedAt.After(results[j].Manifest.CreatedAt)
	})
	return results, nil
}

// ListSnapshots is an explicit alias for List for adapters that expose a
// snapshot collection endpoint.
func (r *Repository) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	return r.List(ctx)
}

// ObjectKey returns the content-addressed key for a file object's SHA-256.
func ObjectKey(checksum string) string {
	return "objects/sha256/" + checksum
}

// ManifestKey returns the immutable key for a manifest's content ID.
func ManifestKey(id string) string {
	return "manifests/sha256/" + id + ".json"
}

func manifestIDFromKey(key string) (string, bool) {
	const prefix = "manifests/sha256/"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".json") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
	if strings.Contains(id, "/") || validateSHA256(id) != nil {
		return "", false
	}
	return id, true
}

func (r *Repository) loadManifest(ctx context.Context, snapshot Snapshot) (Manifest, []byte, error) {
	if err := validateSHA256(snapshot.ID); err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: bad snapshot ID: %v", ErrInvalidSnapshot, err)
	}
	key := ManifestKey(snapshot.ID)
	if snapshot.ManifestKey != "" && snapshot.ManifestKey != key {
		return Manifest{}, nil, fmt.Errorf("%w: manifest key does not match snapshot ID", ErrInvalidSnapshot)
	}
	data, info, err := r.store.Get(ctx, key)
	if err != nil {
		return Manifest{}, nil, err
	}
	if err := checkInfo(key, data, info); err != nil {
		return Manifest{}, nil, err
	}
	if SHA256Hex(data) != snapshot.ID {
		return Manifest{}, nil, fmt.Errorf("%w: manifest ID does not match bytes", ErrChecksumMismatch)
	}
	manifest, err := unmarshalManifest(data)
	if err != nil {
		return Manifest{}, nil, err
	}
	return manifest, data, nil
}

func (r *Repository) putImmutable(ctx context.Context, key string, data []byte, checksum string) error {
	info, created, err := r.store.PutIfAbsent(ctx, key, data, PutOptions{SHA256: checksum})
	if err != nil {
		return err
	}
	if created {
		if err := checkInfo(key, data, info); err != nil {
			return err
		}
		return nil
	}
	// Generic BlobStore implementations may only promise absence/presence. Read
	// existing content ourselves so an unreliable backend cannot silently turn a
	// content-address collision into a valid-looking snapshot.
	existing, existingInfo, err := r.store.Get(ctx, key)
	if err != nil {
		return err
	}
	if err := checkInfo(key, existing, existingInfo); err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return fmt.Errorf("%w: %s", ErrImmutableConflict, key)
	}
	return nil
}

func (r *Repository) getChecked(ctx context.Context, key, checksum string, size int64) ([]byte, error) {
	data, info, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := checkInfo(key, data, info); err != nil {
		return nil, err
	}
	if (size >= 0 && int64(len(data)) != size) || SHA256Hex(data) != checksum {
		return nil, fmt.Errorf("%w", ErrChecksumMismatch)
	}
	return data, nil
}

func checkInfo(key string, data []byte, info BlobInfo) error {
	if info.Key != key {
		return fmt.Errorf("%w: store returned unexpected key", ErrChecksumMismatch)
	}
	if info.Size != int64(len(data)) {
		return fmt.Errorf("%w: store returned unexpected size", ErrChecksumMismatch)
	}
	if info.SHA256 != SHA256Hex(data) {
		return fmt.Errorf("%w: store returned unexpected checksum", ErrChecksumMismatch)
	}
	return nil
}

func marshalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("backup marshal manifest: %w", err)
	}
	return data, nil
}

func unmarshalManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse manifest: %v", ErrInvalidSnapshot, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: manifest has trailing data", ErrInvalidSnapshot)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup canonicalize manifest: %w", err)
	}
	if !bytes.Equal(canonical, data) {
		return Manifest{}, fmt.Errorf("%w: manifest is not canonical JSON", ErrInvalidSnapshot)
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(manifest.AppVersion) == "" || strings.TrimSpace(manifest.SourceID) == "" || manifest.CreatedAt.IsZero() {
		return fmt.Errorf("%w: missing required manifest metadata", ErrInvalidSnapshot)
	}
	previous := ""
	for _, file := range manifest.Files {
		path, err := cleanRelativePath(file.Path)
		if err != nil || path != file.Path {
			return fmt.Errorf("%w: invalid file path", ErrInvalidSnapshot)
		}
		if file.Path <= previous {
			return fmt.Errorf("%w: file entries must be sorted and unique", ErrInvalidSnapshot)
		}
		previous = file.Path
		if err := validateSHA256(file.SHA256); err != nil {
			return fmt.Errorf("%w: invalid file checksum", ErrInvalidSnapshot)
		}
		if file.Size < 0 || file.Mode > 0o777 {
			return fmt.Errorf("%w: invalid file metadata", ErrInvalidSnapshot)
		}
	}
	return nil
}

func readStableFile(filename string, before fs.FileInfo) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("backup snapshot open source file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("backup snapshot stat source file: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%w: source changed to unsupported entry", ErrUnsupportedEntry)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("backup snapshot read source file: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("backup snapshot re-stat source file: %w", err)
	}
	if before.Size() != opened.Size() || opened.Size() != after.Size() || int64(len(data)) != after.Size() || before.Mode().Perm() != opened.Mode().Perm() {
		return nil, fmt.Errorf("backup snapshot source file changed while reading")
	}
	return data, nil
}

func cleanRelativePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%w: empty or malformed path", ErrUnsafePath)
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("%w: absolute path", ErrUnsafePath)
	}
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return "", fmt.Errorf("%w: malformed path", ErrUnsafePath)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: traversal path", ErrUnsafePath)
		}
	}
	clean := strings.Join(parts, "/")
	if clean != value {
		return "", fmt.Errorf("%w: non-canonical path", ErrUnsafePath)
	}
	return clean, nil
}

func defaultExcluded(relative string) bool {
	parts := strings.Split(strings.ToLower(relative), "/")
	// backup.json contains the remote profile and opaque secret references. It
	// is intentionally excluded even though references themselves are not
	// secret material: a snapshot must remain safe to hand to a different
	// machine without changing that machine's backup destination.
	if (len(parts) == 1 && parts[0] == "backup.json") ||
		(len(parts) == 2 && parts[0] == ".carbon" && parts[1] == "backup.json") {
		return true
	}
	// Legacy live state remains excluded when reading old source trees; canonical
	// Carbon live state is excluded for the same reason.
	if len(parts) >= 2 && ((parts[0] == ".cairn" && parts[1] == "live") || (parts[0] == ".carbon" && parts[1] == "live")) {
		return true
	}
	if len(parts) >= 2 && parts[0] == ".carbon" {
		switch parts[1] {
		case "locks", "lock", "staging", "stage", "tmp", "temp", "cache":
			return true
		}
	}
	for _, part := range parts {
		switch part {
		case ".cache", "cache", "backups", "node_modules", "__pycache__", ".git", ".aws", ".ssh", "credentials", "credential", "secrets", "secret":
			return true
		}
	}
	base := parts[len(parts)-1]
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "write.lock" || strings.HasPrefix(base, ".tmp") || strings.HasPrefix(base, "tmp-") || strings.HasPrefix(base, "cairn-restore-") || strings.HasPrefix(base, "carbon-restore-") || strings.HasPrefix(base, "carbon-stage-") || strings.HasPrefix(base, "~$") {
		return true
	}
	for _, suffix := range []string{".lock", ".lck", ".tmp", ".temp", ".swp", ".swo", ".pem", ".key", ".p12", ".pfx", ".kdbx"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, marker := range []string{"credential", "secret", "token", "password", "apikey", "api-key"} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	for _, exact := range []string{"id_rsa", "id_ed25519", "authorized_keys", "known_hosts"} {
		if base == exact {
			return true
		}
	}
	return false
}
