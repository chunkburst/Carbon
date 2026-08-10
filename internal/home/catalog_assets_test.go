package home

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCatalogPresentationAssetRoundTripIsSeparateFromTokens(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	input := catalogAssetJPEG(t, 11, 7, 82)
	asset, err := PutCatalogPresentationAsset(f.root, CatalogPresentationProject, f.project.ID, "image/jpeg", input)
	if err != nil {
		t.Fatal(err)
	}
	if asset.SHA256 == "" || asset.Width != 11 || asset.Height != 7 || asset.Size <= 0 {
		t.Fatalf("stored asset metadata = %#v", asset)
	}

	stored, got, err := GetCatalogPresentationAsset(f.root, CatalogPresentationProject, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != asset {
		t.Fatalf("GET metadata = %#v, want %#v", got, asset)
	}
	if bytes.Equal(stored, input) {
		t.Fatal("stored custom image retained the original JPEG instead of normalizing to PNG")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || format != "png" || config.Width != 11 || config.Height != 7 {
		t.Fatalf("stored image = format %q, bounds %dx%d, err %v; want PNG 11x7", format, config.Width, config.Height, err)
	}
	assetRoot := filepath.Join(f.root, CarbonDirName, CatalogPresentationAssetDirectory)
	indexPath := filepath.Join(assetRoot, CatalogPresentationAssetIndexFilename)
	blobPath := filepath.Join(assetRoot, CatalogPresentationAssetBlobDirectory, catalogPresentationAssetBlobFilename(asset.SHA256))
	for _, filename := range []string{indexPath, blobPath} {
		info, err := os.Lstat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || isReparsePoint(filename, info) {
			t.Fatalf("asset file %s is not a trusted regular file: %v", filename, info.Mode())
		}
	}
	if _, err := os.Lstat(filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom asset changed legacy token document: %v", err)
	}

	if err := DeleteCatalogPresentationAsset(f.root, CatalogPresentationProject, f.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetCatalogPresentationAsset(f.root, CatalogPresentationProject, f.project.ID); !errors.Is(err, ErrCatalogAssetNotFound) {
		t.Fatalf("GET after clear = %v, want ErrCatalogAssetNotFound", err)
	}
	if _, err := os.Lstat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clearing unreferenced image left blob behind: %v", err)
	}
	if err := DeleteCatalogPresentationAsset(f.root, CatalogPresentationProject, f.project.ID); err != nil {
		t.Fatalf("idempotent clear = %v", err)
	}
}

func TestCatalogPresentationAssetAcceptsActualWebP(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	// A small lossless WebP test vector. The production package imports the WebP
	// decoder itself, so this does not rely on a test-only image registration.
	webpData, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID, "image/webp", webpData)
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := GetCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if config, format, err := image.DecodeConfig(bytes.NewReader(stored)); err != nil || format != "png" || config.Width != asset.Width || config.Height != asset.Height {
		t.Fatalf("stored WebP normalization = format %q %dx%d, metadata %#v, err %v", format, config.Width, config.Height, asset, err)
	}
}

func TestCatalogPresentationAssetTargetValidationCoversNestedAndStandaloneProjects(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	standalone, err := AddStandaloneProject(f.root, AddProjectRequest{
		Name: "Standalone", Kind: ProjectWeb, SourcePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{f.project.ID, standalone.ID} {
		if _, err := PutCatalogPresentationAsset(f.root, CatalogPresentationProject, projectID, "image/png", catalogAssetPNG(t, 4, 3, byte(len(projectID)))); err != nil {
			t.Fatalf("project %s asset PUT = %v", projectID, err)
		}
		if _, _, err := GetCatalogPresentationAsset(f.root, CatalogPresentationProject, projectID); err != nil {
			t.Fatalf("project %s asset GET = %v", projectID, err)
		}
	}
	unknown := "project_" + strings.Repeat("0", 32)
	if _, err := PutCatalogPresentationAsset(f.root, CatalogPresentationProject, unknown, "image/png", catalogAssetPNG(t, 2, 2, 1)); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project asset PUT = %v, want ErrProjectNotFound", err)
	}
}

func TestCatalogPresentationAssetRejectsSpoofsCorruptionAndBoundsWithoutMutation(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	baseline, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID, "image/png", catalogAssetPNG(t, 8, 8, 11))
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(f.root, CarbonDirName, CatalogPresentationAssetDirectory, CatalogPresentationAssetIndexFilename)
	beforeIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBlob, _, err := GetCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	validPNG := catalogAssetPNG(t, 7, 5, 21)
	validGIF := catalogAssetGIF(t)
	pixelBomb := catalogAssetSolidPNG(t, 1025, 1024)
	tooWide := catalogAssetSolidPNG(t, MaxCatalogPresentationAssetDimension+1, 1)
	tooLargeOutput := catalogAssetNoisyJPEG(t, 800, 800)
	if int64(len(tooLargeOutput)) > MaxCatalogPresentationAssetBytes {
		t.Fatalf("test JPEG unexpectedly exceeds raw limit: %d", len(tooLargeOutput))
	}
	for _, test := range []struct {
		name        string
		contentType string
		data        []byte
	}{
		{"MIME spoof", "image/jpeg", validPNG},
		{"corrupt bytes", "image/png", []byte("this is not an image")},
		{"SVG", "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"data URL", "image/png", []byte("data:image/png;base64,iVBORw0KGgo=")},
		{"URL/path text", "image/png", []byte("https://example.test/icon.png")},
		{"GIF", "image/png", validGIF},
		{"oversized raw body", "image/png", bytes.Repeat([]byte{0}, int(MaxCatalogPresentationAssetBytes+1))},
		{"dimension bomb", "image/png", tooWide},
		{"pixel bomb", "image/png", pixelBomb},
		{"normalized output over limit", "image/jpeg", tooLargeOutput},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID, test.contentType, test.data); !errors.Is(err, ErrInvalidCatalogAsset) {
				t.Fatalf("PutCatalogPresentationAsset = %v, want ErrInvalidCatalogAsset", err)
			}
		})
	}
	afterIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	afterBlob, afterMetadata, err := GetCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeIndex, afterIndex) || !bytes.Equal(beforeBlob, afterBlob) || afterMetadata != baseline {
		t.Fatalf("invalid asset upload changed durable state\nmetadata=%#v want %#v\nindex before=%s\nafter=%s", afterMetadata, baseline, beforeIndex, afterIndex)
	}
}

func TestCatalogPresentationAssetRejectsTraversalAndReparsePaths(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	data := catalogAssetPNG(t, 4, 4, 1)
	for _, id := range []string{"../outside", "project_" + strings.Repeat("0", 32), "cluster_" + strings.Repeat("0", 32)} {
		if _, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, id, "image/png", data); !errors.Is(err, ErrInvalidCatalogPresentationTarget) && !errors.Is(err, ErrClusterNotFound) {
			t.Fatalf("traversal/unknown target %q = %v", id, err)
		}
	}

	assetRoot := filepath.Join(f.root, CarbonDirName, CatalogPresentationAssetDirectory)
	if err := os.Symlink(t.TempDir(), assetRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID, "image/png", data); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("asset directory reparse PUT = %v, want ErrUnsafePath", err)
	}

	f = newCatalogPresentationFixture(t)
	asset, err := PutCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID, "image/png", data)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(f.root, CarbonDirName, CatalogPresentationAssetDirectory, CatalogPresentationAssetBlobDirectory, catalogPresentationAssetBlobFilename(asset.SHA256))
	external := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(external, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, blobPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := GetCatalogPresentationAsset(f.root, CatalogPresentationCluster, f.cluster.ID); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("blob reparse GET = %v, want ErrUnsafePath", err)
	}
}

func TestCatalogPresentationAssetConcurrentWritesAndConservativeCleanup(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	const workers = 12
	clusters := make([]Cluster, 0, workers)
	for i := 0; i < workers; i++ {
		cluster, err := CreateCluster(root, CreateClusterRequest{Name: fmt.Sprintf("Asset %d", i), Prefix: fmt.Sprintf("A%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		clusters = append(clusters, cluster)
	}
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i, cluster := range clusters {
		data := catalogAssetPNG(t, 5+i%3, 4+i%2, byte(i+1))
		wg.Add(1)
		go func(clusterID string, imageData []byte) {
			defer wg.Done()
			<-start
			_, err := PutCatalogPresentationAsset(root, CatalogPresentationCluster, clusterID, "image/png", imageData)
			errs <- err
		}(cluster.ID, data)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent PutCatalogPresentationAsset = %v", err)
		}
	}
	dirs, exists, err := catalogPresentationAssetDirectories(filepath.Join(root, CarbonDirName), false)
	if err != nil || !exists {
		t.Fatalf("asset dirs = %#v, exists=%v, err=%v", dirs, exists, err)
	}
	assets, err := readCatalogPresentationAssets(dirs.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets.Clusters) != workers {
		t.Fatalf("concurrent asset count = %d, want %d: %#v", len(assets.Clusters), workers, assets.Clusters)
	}

	// A non-Carbon file, and even a hash-looking filename whose contents do not
	// match that hash, must survive cleanup. Only verified generated orphan PNGs
	// are eligible for removal.
	unknown := filepath.Join(dirs.blobs, "readme.txt")
	lookalike := filepath.Join(dirs.blobs, strings.Repeat("a", 64)+".png")
	if err := os.WriteFile(unknown, []byte("leave me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lookalike, []byte("not this hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := clusters[0]
	old, _, err := GetCatalogPresentationAsset(root, CatalogPresentationCluster, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := sha256Hex(old)
	if _, err := PutCatalogPresentationAsset(root, CatalogPresentationCluster, first.ID, "image/png", catalogAssetPNG(t, 9, 9, 99)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dirs.blobs, catalogPresentationAssetBlobFilename(oldHash))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced unreferenced blob remained: %v", err)
	}
	for _, filename := range []string{unknown, lookalike} {
		if _, err := os.Lstat(filename); err != nil {
			t.Fatalf("conservative cleanup removed %s: %v", filename, err)
		}
	}
}

func catalogAssetPNG(t *testing.T, width, height int, seed byte) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.SetRGBA(x, y, color.RGBA{R: seed + byte(x), G: seed + byte(y), B: seed ^ byte(x+y), A: 0xff})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, imageData); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func catalogAssetSolidPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	var output bytes.Buffer
	if err := png.Encode(&output, imageData); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func catalogAssetJPEG(t *testing.T, width, height int, seed byte) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.SetRGBA(x, y, color.RGBA{R: seed + byte(x), G: seed + byte(y), B: seed ^ byte(x+y), A: 0xff})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, imageData, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func catalogAssetNoisyJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// A deterministic high-entropy pattern stays small at JPEG quality 1 but
			// expands beyond the normalized PNG byte budget.
			v := uint32(x*1103515245 + y*12345)
			imageData.SetRGBA(x, y, color.RGBA{R: byte(v), G: byte(v >> 8), B: byte(v >> 16), A: 0xff})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, imageData, &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func catalogAssetGIF(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	imageData.SetColorIndex(1, 1, 1)
	var output bytes.Buffer
	if err := gif.Encode(&output, imageData, nil); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
