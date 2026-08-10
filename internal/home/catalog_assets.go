package home

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

const (
	// CatalogPresentationAssetDirectory is a private Carbon-home child. It is
	// intentionally separate from catalog-presentation.json, whose established
	// builtin/emoji token wire format remains backwards compatible.
	CatalogPresentationAssetDirectory = "catalog-assets"
	// CatalogPresentationAssetBlobDirectory holds content-addressed, normalized
	// PNG blobs below CatalogPresentationAssetDirectory.
	CatalogPresentationAssetBlobDirectory = "blobs"
	// CatalogPresentationAssetIndexFilename maps stable catalog IDs to blobs.
	CatalogPresentationAssetIndexFilename = "index.json"
	// CatalogPresentationAssetVersion is the only supported asset-index schema.
	CatalogPresentationAssetVersion = 1

	// MaxCatalogPresentationAssetBytes bounds both the submitted image and its
	// normalized PNG. It is intentionally the same global 1 MiB ceiling as
	// other Carbon metadata so a catalog decoration can never become a storage
	// side channel.
	MaxCatalogPresentationAssetBytes int64 = 1 << 20
	// MaxCatalogPresentationAssetDimension bounds either decoded dimension. The
	// independent pixel cap below is deliberately tighter for ordinary square
	// images, so a narrow but valid source is not mistaken for a pixel bomb.
	MaxCatalogPresentationAssetDimension = 4096
	// MaxCatalogPresentationAssetPixels bounds decoded allocation independently
	// of compressed source size.
	MaxCatalogPresentationAssetPixels int64 = 1024 * 1024
)

var (
	// ErrInvalidCatalogAsset means image bytes, image metadata, or the asset
	// index failed a strict validation check. Callers must not serve or rewrite
	// the affected state.
	ErrInvalidCatalogAsset = errors.New("invalid Carbon catalog asset")
	// ErrFutureCatalogAssetVersion prevents an older binary from rewriting an
	// asset index containing fields it cannot preserve.
	ErrFutureCatalogAssetVersion = errors.New("unsupported future Carbon catalog asset version")
	// ErrCatalogAssetNotFound is the normal GET result when a catalog target has
	// no custom image. It is distinct from an unknown cluster/project target.
	ErrCatalogAssetNotFound = errors.New("Carbon catalog asset not found")

	errCatalogAssetOutputTooLarge = errors.New("normalized catalog asset exceeds byte limit")
)

// CatalogPresentationAsset is the immutable metadata persisted for one custom
// image. Every blob is normalized to PNG before hashing, so no source filename,
// original MIME type, EXIF data, or untrusted path ever reaches disk.
type CatalogPresentationAsset struct {
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Size   int64  `json:"size"`
}

// CatalogPresentationAssets is the private v1 asset index. It deliberately has
// its own document instead of adding a new icon kind to catalog-presentation.json.
type CatalogPresentationAssets struct {
	Version  int                                 `json:"version"`
	Clusters map[string]CatalogPresentationAsset `json:"clusters"`
	Projects map[string]CatalogPresentationAsset `json:"projects"`
}

type catalogPresentationAssetWire struct {
	Version  *int            `json:"version"`
	Clusters json.RawMessage `json:"clusters"`
	Projects json.RawMessage `json:"projects"`
}

type catalogPresentationAssetDirs struct {
	root  string
	blobs string
}

type normalizedCatalogPresentationAsset struct {
	metadata CatalogPresentationAsset
	png      []byte
}

// PutCatalogPresentationAsset validates an uploaded PNG, JPEG, or WebP image,
// strips all source metadata by re-encoding it as PNG, and atomically associates
// the resulting content-addressed blob with one live cluster/project target.
// declaredContentType must match the actual decoded input format.
func PutCatalogPresentationAsset(main string, kind CatalogPresentationKind, id, declaredContentType string, data []byte) (CatalogPresentationAsset, error) {
	if err := validateCatalogPresentationTarget(kind, id); err != nil {
		return CatalogPresentationAsset{}, err
	}
	normalized, err := normalizeCatalogPresentationAsset(data, declaredContentType)
	if err != nil {
		return CatalogPresentationAsset{}, err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return CatalogPresentationAsset{}, err
	}

	var result CatalogPresentationAsset
	err = withLock(root, func() error {
		carbonRoot, manifest, err := catalogPresentationHome(root)
		if err != nil {
			return err
		}
		if err := ensureCatalogPresentationTarget(manifest, kind, id); err != nil {
			return err
		}
		dirs, _, err := catalogPresentationAssetDirectories(carbonRoot, true)
		if err != nil {
			return err
		}
		assets, err := readCatalogPresentationAssets(dirs.root)
		if err != nil {
			return err
		}
		if err := writeCatalogPresentationAssetBlob(dirs.blobs, normalized.metadata, normalized.png); err != nil {
			return err
		}

		targets := catalogPresentationAssetTargetMap(&assets, kind)
		if previous, exists := targets[id]; !exists || previous != normalized.metadata {
			targets[id] = normalized.metadata
			if err := writeCatalogPresentationAssets(dirs.root, assets); err != nil {
				return err
			}
		}
		// A successful, durable index write makes old unreferenced content safe to
		// collect. Cleanup never touches malformed, unknown, or reparse entries.
		if err := cleanupCatalogPresentationAssetOrphans(dirs, assets); err != nil {
			return err
		}
		result = normalized.metadata
		return nil
	})
	if err != nil {
		return CatalogPresentationAsset{}, err
	}
	return result, nil
}

// GetCatalogPresentationAsset returns the verified normalized PNG for one live
// catalog target. A missing association returns ErrCatalogAssetNotFound; a stale
// or tampered index/blob fails closed as ErrInvalidCatalogAsset or ErrUnsafePath.
func GetCatalogPresentationAsset(main string, kind CatalogPresentationKind, id string) ([]byte, CatalogPresentationAsset, error) {
	if err := validateCatalogPresentationTarget(kind, id); err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	carbonRoot, manifest, err := catalogPresentationHome(root)
	if err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	if err := ensureCatalogPresentationTarget(manifest, kind, id); err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	dirs, exists, err := catalogPresentationAssetDirectories(carbonRoot, false)
	if err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	if !exists {
		return nil, CatalogPresentationAsset{}, ErrCatalogAssetNotFound
	}
	assets, err := readCatalogPresentationAssets(dirs.root)
	if err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	metadata, exists := catalogPresentationAssetTargetMap(&assets, kind)[id]
	if !exists {
		return nil, CatalogPresentationAsset{}, ErrCatalogAssetNotFound
	}
	if _, err := os.Lstat(dirs.blobs); errors.Is(err, os.ErrNotExist) {
		return nil, CatalogPresentationAsset{}, fmt.Errorf("%w: blob directory is missing", ErrInvalidCatalogAsset)
	} else if err != nil {
		return nil, CatalogPresentationAsset{}, fmt.Errorf("%w: inspect blob directory: %v", ErrUnsafePath, err)
	}
	data, err := readCatalogPresentationAssetBlob(dirs.blobs, metadata)
	if err != nil {
		return nil, CatalogPresentationAsset{}, err
	}
	return data, metadata, nil
}

// DeleteCatalogPresentationAsset clears a custom image association. It is
// intentionally idempotent: clearing a target with no image does not create any
// asset metadata and still succeeds. Unreferenced known PNG blobs are collected
// only after the new index has been atomically persisted.
func DeleteCatalogPresentationAsset(main string, kind CatalogPresentationKind, id string) error {
	if err := validateCatalogPresentationTarget(kind, id); err != nil {
		return err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return err
	}
	return withLock(root, func() error {
		carbonRoot, manifest, err := catalogPresentationHome(root)
		if err != nil {
			return err
		}
		if err := ensureCatalogPresentationTarget(manifest, kind, id); err != nil {
			return err
		}
		dirs, exists, err := catalogPresentationAssetDirectories(carbonRoot, false)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		assets, err := readCatalogPresentationAssets(dirs.root)
		if err != nil {
			return err
		}
		targets := catalogPresentationAssetTargetMap(&assets, kind)
		if _, exists := targets[id]; !exists {
			return nil
		}
		delete(targets, id)
		if err := writeCatalogPresentationAssets(dirs.root, assets); err != nil {
			return err
		}
		return cleanupCatalogPresentationAssetOrphans(dirs, assets)
	})
}

// PutCatalogPresentationAsset is the Home-handle equivalent of the package
// function. It retains the home-only ownership boundary at the package layer.
func (h *Home) PutCatalogPresentationAsset(kind CatalogPresentationKind, id, declaredContentType string, data []byte) (CatalogPresentationAsset, error) {
	if h == nil {
		return CatalogPresentationAsset{}, ErrNotInitialized
	}
	return PutCatalogPresentationAsset(h.Root, kind, id, declaredContentType, data)
}

// CatalogPresentationAsset returns one custom catalog image from this Home.
func (h *Home) CatalogPresentationAsset(kind CatalogPresentationKind, id string) ([]byte, CatalogPresentationAsset, error) {
	if h == nil {
		return nil, CatalogPresentationAsset{}, ErrNotInitialized
	}
	return GetCatalogPresentationAsset(h.Root, kind, id)
}

// DeleteCatalogPresentationAsset is the Home-handle equivalent of the package
// function.
func (h *Home) DeleteCatalogPresentationAsset(kind CatalogPresentationKind, id string) error {
	if h == nil {
		return ErrNotInitialized
	}
	return DeleteCatalogPresentationAsset(h.Root, kind, id)
}

func normalizeCatalogPresentationAsset(data []byte, declaredContentType string) (normalizedCatalogPresentationAsset, error) {
	if len(data) == 0 {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: image body is required", ErrInvalidCatalogAsset)
	}
	if int64(len(data)) > MaxCatalogPresentationAssetBytes {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: image exceeds %d bytes", ErrInvalidCatalogAsset, MaxCatalogPresentationAssetBytes)
	}
	declared, err := canonicalCatalogPresentationAssetContentType(declaredContentType)
	if err != nil {
		return normalizedCatalogPresentationAsset{}, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: decode image config: %v", ErrInvalidCatalogAsset, err)
	}
	actual, ok := catalogPresentationAssetFormatContentType(format)
	if !ok {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: unsupported decoded image format %q", ErrInvalidCatalogAsset, format)
	}
	if actual != declared {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: declared %s does not match decoded %s", ErrInvalidCatalogAsset, declared, actual)
	}
	if err := validateCatalogPresentationAssetDimensions(config.Width, config.Height); err != nil {
		return normalizedCatalogPresentationAsset{}, err
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: decode image: %v", ErrInvalidCatalogAsset, err)
	}
	if decodedFormat != format {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: inconsistent decoded image format", ErrInvalidCatalogAsset)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: inconsistent decoded image dimensions", ErrInvalidCatalogAsset)
	}
	if err := validateCatalogPresentationAssetDimensions(bounds.Dx(), bounds.Dy()); err != nil {
		return normalizedCatalogPresentationAsset{}, err
	}

	var output limitedCatalogAssetWriter
	output.limit = MaxCatalogPresentationAssetBytes
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&output, decoded); err != nil {
		if errors.Is(err, errCatalogAssetOutputTooLarge) {
			return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: normalized PNG exceeds %d bytes", ErrInvalidCatalogAsset, MaxCatalogPresentationAssetBytes)
		}
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: encode normalized PNG: %v", ErrInvalidCatalogAsset, err)
	}
	pngData := output.buffer.Bytes()
	if len(pngData) == 0 || int64(len(pngData)) > MaxCatalogPresentationAssetBytes {
		return normalizedCatalogPresentationAsset{}, fmt.Errorf("%w: normalized PNG exceeds %d bytes", ErrInvalidCatalogAsset, MaxCatalogPresentationAssetBytes)
	}
	sum := sha256.Sum256(pngData)
	metadata := CatalogPresentationAsset{
		SHA256: hex.EncodeToString(sum[:]),
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
		Size:   int64(len(pngData)),
	}
	if err := validateCatalogPresentationAssetMetadata(metadata); err != nil {
		return normalizedCatalogPresentationAsset{}, err
	}
	return normalizedCatalogPresentationAsset{metadata: metadata, png: append([]byte(nil), pngData...)}, nil
}

type limitedCatalogAssetWriter struct {
	buffer bytes.Buffer
	limit  int64
}

func (w *limitedCatalogAssetWriter) Write(p []byte) (int, error) {
	remaining := w.limit - int64(w.buffer.Len())
	if remaining <= 0 {
		return 0, errCatalogAssetOutputTooLarge
	}
	if int64(len(p)) > remaining {
		_, _ = w.buffer.Write(p[:int(remaining)])
		return int(remaining), errCatalogAssetOutputTooLarge
	}
	return w.buffer.Write(p)
}

func canonicalCatalogPresentationAssetContentType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", fmt.Errorf("%w: invalid image Content-Type", ErrInvalidCatalogAsset)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
		return mediaType, nil
	default:
		return "", fmt.Errorf("%w: unsupported image Content-Type %q", ErrInvalidCatalogAsset, mediaType)
	}
}

func catalogPresentationAssetFormatContentType(format string) (string, bool) {
	switch format {
	case "png":
		return "image/png", true
	case "jpeg":
		return "image/jpeg", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func validateCatalogPresentationAssetDimensions(width, height int) error {
	if width <= 0 || height <= 0 || width > MaxCatalogPresentationAssetDimension || height > MaxCatalogPresentationAssetDimension ||
		int64(width)*int64(height) > MaxCatalogPresentationAssetPixels {
		return fmt.Errorf("%w: image dimensions %dx%d exceed %dx%d / %d pixels", ErrInvalidCatalogAsset, width, height, MaxCatalogPresentationAssetDimension, MaxCatalogPresentationAssetDimension, MaxCatalogPresentationAssetPixels)
	}
	return nil
}

func catalogPresentationAssetDirectories(carbonRoot string, create bool) (catalogPresentationAssetDirs, bool, error) {
	root := filepath.Join(carbonRoot, CatalogPresentationAssetDirectory)
	exists, err := ensureCatalogPresentationAssetDirectory(carbonRoot, root, create)
	if err != nil || !exists {
		return catalogPresentationAssetDirs{}, exists, err
	}
	blobs := filepath.Join(root, CatalogPresentationAssetBlobDirectory)
	if _, err := ensureCatalogPresentationAssetDirectory(root, blobs, create); err != nil {
		return catalogPresentationAssetDirs{}, false, err
	}
	return catalogPresentationAssetDirs{root: root, blobs: blobs}, true, nil
}

// ensureCatalogPresentationAssetDirectory only accepts a direct, real directory
// within its trusted parent. It deliberately avoids MkdirAll so no intermediate
// component can be followed through a symlink/junction/reparse point.
func ensureCatalogPresentationAssetDirectory(parent, directory string, create bool) (bool, error) {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return false, nil
		}
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("carbon: create catalog asset directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect catalog asset directory %s: %v", ErrUnsafePath, directory, err)
	}
	if isReparsePoint(directory, info) || !info.IsDir() {
		return false, fmt.Errorf("%w: refusing catalog asset directory %s", ErrUnsafePath, directory)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || !samePath(resolved, directory) || !pathWithin(parent, resolved) {
		return false, fmt.Errorf("%w: catalog asset directory escapes trusted parent", ErrUnsafePath)
	}
	return true, nil
}

func catalogPresentationAssetIndexPath(assetRoot string) string {
	return filepath.Join(assetRoot, CatalogPresentationAssetIndexFilename)
}

func readCatalogPresentationAssets(assetRoot string) (CatalogPresentationAssets, error) {
	filename := catalogPresentationAssetIndexPath(assetRoot)
	if _, exists, err := safeRegularFile(assetRoot, filename, true); err != nil {
		return CatalogPresentationAssets{}, err
	} else if !exists {
		return emptyCatalogPresentationAssets(), nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return CatalogPresentationAssets{}, fmt.Errorf("carbon: open catalog asset index: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxCatalogPresentationAssetBytes+1))
	if err != nil {
		return CatalogPresentationAssets{}, fmt.Errorf("carbon: read catalog asset index: %w", err)
	}
	if int64(len(data)) > MaxCatalogPresentationAssetBytes {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: asset index exceeds %d bytes", ErrInvalidCatalogAsset, MaxCatalogPresentationAssetBytes)
	}
	return decodeCatalogPresentationAssets(data)
}

func decodeCatalogPresentationAssets(data []byte) (CatalogPresentationAssets, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: ambiguous JSON", ErrInvalidCatalogAsset)
	}
	var wire catalogPresentationAssetWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidCatalogAsset, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return CatalogPresentationAssets{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidCatalogAsset)
		}
		return CatalogPresentationAssets{}, fmt.Errorf("%w: parse JSON: %v", ErrInvalidCatalogAsset, err)
	}
	if wire.Version == nil || len(wire.Clusters) == 0 || len(wire.Projects) == 0 ||
		bytes.Equal(bytes.TrimSpace(wire.Clusters), []byte("null")) ||
		bytes.Equal(bytes.TrimSpace(wire.Projects), []byte("null")) {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: version, clusters, and projects are required", ErrInvalidCatalogAsset)
	}
	if *wire.Version > CatalogPresentationAssetVersion {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: version %d", ErrFutureCatalogAssetVersion, *wire.Version)
	}
	if *wire.Version != CatalogPresentationAssetVersion {
		return CatalogPresentationAssets{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidCatalogAsset, *wire.Version)
	}
	clusters, err := decodeCatalogPresentationAssetMap(wire.Clusters, "clusters")
	if err != nil {
		return CatalogPresentationAssets{}, err
	}
	projects, err := decodeCatalogPresentationAssetMap(wire.Projects, "projects")
	if err != nil {
		return CatalogPresentationAssets{}, err
	}
	assets := CatalogPresentationAssets{Version: *wire.Version, Clusters: clusters, Projects: projects}
	if err := validateCatalogPresentationAssets(assets); err != nil {
		return CatalogPresentationAssets{}, err
	}
	return assets, nil
}

func decodeCatalogPresentationAssetMap(raw json.RawMessage, field string) (map[string]CatalogPresentationAsset, error) {
	var assets map[string]CatalogPresentationAsset
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assets); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrInvalidCatalogAsset, field, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: parse %s: multiple JSON values", ErrInvalidCatalogAsset, field)
		}
		return nil, fmt.Errorf("%w: parse %s: %v", ErrInvalidCatalogAsset, field, err)
	}
	if assets == nil {
		return nil, fmt.Errorf("%w: %s must be an object", ErrInvalidCatalogAsset, field)
	}
	return assets, nil
}

func writeCatalogPresentationAssets(assetRoot string, assets CatalogPresentationAssets) error {
	if err := validateCatalogPresentationAssets(assets); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cloneCatalogPresentationAssets(assets), "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode catalog asset index: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteCatalogPresentationAssetFile(assetRoot, CatalogPresentationAssetIndexFilename, data)
}

func atomicWriteCatalogPresentationAssetFile(directory, filename string, data []byte) error {
	if filepath.Base(filename) != filename || filename == "" || filename == "." || filename == ".." {
		return fmt.Errorf("%w: invalid catalog asset filename %q", ErrUnsafePath, filename)
	}
	target := filepath.Join(directory, filename)
	if _, _, err := safeRegularFile(directory, target, true); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".catalog-asset-*.tmp")
	if err != nil {
		return fmt.Errorf("carbon: create catalog asset temp: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("carbon: chmod catalog asset temp: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("carbon: write catalog asset temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("carbon: sync catalog asset temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("carbon: close catalog asset temp: %w", err)
	}
	if _, _, err := safeRegularFile(directory, target, true); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("carbon: replace catalog asset file: %w", err)
	}
	if _, _, err := safeRegularFile(directory, target, false); err != nil {
		return err
	}
	syncCatalogPresentationAssetDirectory(directory)
	return nil
}

func writeCatalogPresentationAssetBlob(blobRoot string, metadata CatalogPresentationAsset, data []byte) error {
	if err := validateCatalogPresentationAssetMetadata(metadata); err != nil {
		return err
	}
	if int64(len(data)) != metadata.Size {
		return fmt.Errorf("%w: normalized blob size mismatch", ErrInvalidCatalogAsset)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != metadata.SHA256 {
		return fmt.Errorf("%w: normalized blob hash mismatch", ErrInvalidCatalogAsset)
	}
	filename := catalogPresentationAssetBlobFilename(metadata.SHA256)
	target := filepath.Join(blobRoot, filename)
	if _, exists, err := safeRegularFile(blobRoot, target, true); err != nil {
		return err
	} else if exists {
		stored, err := readCatalogPresentationAssetBlob(blobRoot, metadata)
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, data) {
			return fmt.Errorf("%w: content-addressed blob disagrees with normalized bytes", ErrInvalidCatalogAsset)
		}
		return nil
	}
	return atomicWriteCatalogPresentationAssetFile(blobRoot, filename, data)
}

func readCatalogPresentationAssetBlob(blobRoot string, metadata CatalogPresentationAsset) ([]byte, error) {
	if err := validateCatalogPresentationAssetMetadata(metadata); err != nil {
		return nil, err
	}
	filename := catalogPresentationAssetBlobFilename(metadata.SHA256)
	target := filepath.Join(blobRoot, filename)
	if _, exists, err := safeRegularFile(blobRoot, target, false); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("%w: blob %s is missing", ErrInvalidCatalogAsset, metadata.SHA256)
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("carbon: open catalog asset blob: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxCatalogPresentationAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("carbon: read catalog asset blob: %w", err)
	}
	if int64(len(data)) != metadata.Size || int64(len(data)) > MaxCatalogPresentationAssetBytes {
		return nil, fmt.Errorf("%w: stored blob size mismatch", ErrInvalidCatalogAsset)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != metadata.SHA256 {
		return nil, fmt.Errorf("%w: stored blob hash mismatch", ErrInvalidCatalogAsset)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" {
		return nil, fmt.Errorf("%w: stored blob is not a valid PNG", ErrInvalidCatalogAsset)
	}
	if config.Width != metadata.Width || config.Height != metadata.Height {
		return nil, fmt.Errorf("%w: stored blob dimensions mismatch", ErrInvalidCatalogAsset)
	}
	if err := validateCatalogPresentationAssetDimensions(config.Width, config.Height); err != nil {
		return nil, err
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != "png" || decoded.Bounds().Dx() != metadata.Width || decoded.Bounds().Dy() != metadata.Height {
		return nil, fmt.Errorf("%w: stored blob cannot be decoded as its declared PNG", ErrInvalidCatalogAsset)
	}
	return data, nil
}

func cleanupCatalogPresentationAssetOrphans(dirs catalogPresentationAssetDirs, assets CatalogPresentationAssets) error {
	if err := validateCatalogPresentationAssets(assets); err != nil {
		return err
	}
	entries, err := os.ReadDir(dirs.blobs)
	if err != nil {
		return fmt.Errorf("carbon: list catalog asset blobs: %w", err)
	}
	referenced := make(map[string]struct{}, len(assets.Clusters)+len(assets.Projects))
	for _, metadata := range assets.Clusters {
		referenced[metadata.SHA256] = struct{}{}
	}
	for _, metadata := range assets.Projects {
		referenced[metadata.SHA256] = struct{}{}
	}
	for _, entry := range entries {
		name := entry.Name()
		hash, ok := catalogPresentationAssetHashFromBlobFilename(name)
		if !ok {
			continue
		}
		if _, keep := referenced[hash]; keep {
			continue
		}
		filename := filepath.Join(dirs.blobs, name)
		info, err := os.Lstat(filename)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("%w: inspect catalog asset orphan %s: %v", ErrUnsafePath, filename, err)
		}
		// Be conservative: only remove our own exact generated filename when it
		// remains an ordinary file. Reparse, directories, and unknown content are
		// left untouched rather than followed or deleted.
		if isReparsePoint(filename, info) || !info.Mode().IsRegular() {
			continue
		}
		if !isVerifiedCatalogPresentationAssetOrphan(filename, hash) {
			continue
		}
		if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("carbon: remove orphaned catalog asset: %w", err)
		}
	}
	syncCatalogPresentationAssetDirectory(dirs.blobs)
	return nil
}

// isVerifiedCatalogPresentationAssetOrphan recognizes only the normalized blobs
// produced by this package. A manually placed file that merely has a hash-looking
// name is intentionally left alone during cleanup.
func isVerifiedCatalogPresentationAssetOrphan(filename, hash string) bool {
	f, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxCatalogPresentationAssetBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > MaxCatalogPresentationAssetBytes {
		return false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return false
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || validateCatalogPresentationAssetDimensions(config.Width, config.Height) != nil {
		return false
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	return err == nil && decodedFormat == "png" && decoded.Bounds().Dx() == config.Width && decoded.Bounds().Dy() == config.Height
}

func syncCatalogPresentationAssetDirectory(directory string) {
	f, err := os.Open(directory)
	if err != nil {
		return
	}
	_ = f.Sync() // best effort on Windows
	_ = f.Close()
}

func validateCatalogPresentationAssets(assets CatalogPresentationAssets) error {
	if assets.Version > CatalogPresentationAssetVersion {
		return fmt.Errorf("%w: version %d", ErrFutureCatalogAssetVersion, assets.Version)
	}
	if assets.Version != CatalogPresentationAssetVersion || assets.Clusters == nil || assets.Projects == nil {
		return fmt.Errorf("%w: version, clusters, and projects are required", ErrInvalidCatalogAsset)
	}
	if len(assets.Clusters) > maxCatalogPresentationEntries || len(assets.Projects) > maxCatalogPresentationEntries {
		return fmt.Errorf("%w: clusters and projects must contain at most %d entries", ErrInvalidCatalogAsset, maxCatalogPresentationEntries)
	}
	for id, metadata := range assets.Clusters {
		if !validID(id, "cluster") {
			return fmt.Errorf("%w: invalid cluster id %q", ErrInvalidCatalogAsset, id)
		}
		if err := validateCatalogPresentationAssetMetadata(metadata); err != nil {
			return fmt.Errorf("%w: cluster %s: %v", ErrInvalidCatalogAsset, id, err)
		}
	}
	for id, metadata := range assets.Projects {
		if !validID(id, "project") {
			return fmt.Errorf("%w: invalid project id %q", ErrInvalidCatalogAsset, id)
		}
		if err := validateCatalogPresentationAssetMetadata(metadata); err != nil {
			return fmt.Errorf("%w: project %s: %v", ErrInvalidCatalogAsset, id, err)
		}
	}
	return nil
}

func validateCatalogPresentationAssetMetadata(metadata CatalogPresentationAsset) error {
	if !validCatalogPresentationAssetHash(metadata.SHA256) || metadata.Size <= 0 || metadata.Size > MaxCatalogPresentationAssetBytes {
		return fmt.Errorf("%w: invalid catalog asset metadata", ErrInvalidCatalogAsset)
	}
	return validateCatalogPresentationAssetDimensions(metadata.Width, metadata.Height)
}

func validCatalogPresentationAssetHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func catalogPresentationAssetBlobFilename(hash string) string {
	return hash + ".png"
}

func catalogPresentationAssetHashFromBlobFilename(name string) (string, bool) {
	if !strings.HasSuffix(name, ".png") {
		return "", false
	}
	hash := strings.TrimSuffix(name, ".png")
	return hash, validCatalogPresentationAssetHash(hash)
}

func catalogPresentationAssetTargetMap(assets *CatalogPresentationAssets, kind CatalogPresentationKind) map[string]CatalogPresentationAsset {
	switch kind {
	case CatalogPresentationCluster:
		return assets.Clusters
	case CatalogPresentationProject:
		return assets.Projects
	default:
		return map[string]CatalogPresentationAsset{}
	}
}

func emptyCatalogPresentationAssets() CatalogPresentationAssets {
	return CatalogPresentationAssets{
		Version:  CatalogPresentationAssetVersion,
		Clusters: map[string]CatalogPresentationAsset{},
		Projects: map[string]CatalogPresentationAsset{},
	}
}

func cloneCatalogPresentationAssets(assets CatalogPresentationAssets) CatalogPresentationAssets {
	clone := emptyCatalogPresentationAssets()
	clone.Version = assets.Version
	for id, metadata := range assets.Clusters {
		clone.Clusters[id] = metadata
	}
	for id, metadata := range assets.Projects {
		clone.Projects[id] = metadata
	}
	return clone
}
