package home

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"carbon/internal/backup"
)

type catalogPresentationFixture struct {
	root    string
	cluster Cluster
	project Project
}

func newCatalogPresentationFixture(t *testing.T) catalogPresentationFixture {
	t.Helper()
	useHomeTestCache(t)
	root := t.TempDir()
	cluster, err := CreateCluster(root, CreateClusterRequest{Name: "Catalog", Prefix: "CAT"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := AddProject(root, cluster.ID, AddProjectRequest{
		Name: "Desktop", Kind: ProjectPC, SourcePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalogPresentationFixture{root: root, cluster: cluster, project: project}
}

func TestCatalogPresentationMissingFileIsEmptyAndDoesNotWrite(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}

	presentation, err := ListCatalogPresentation(root)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Version != CatalogPresentationVersion || len(presentation.Clusters) != 0 || len(presentation.Projects) != 0 {
		t.Fatalf("missing presentation = %#v, want v1 empty maps", presentation)
	}
	path := filepath.Join(root, CarbonDirName, CatalogPresentationFilename)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing presentation read created metadata: %v", err)
	}
}

func TestCatalogPresentationRoundTripClearAndLeaveManifestUntouched(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	manifestPath := filepath.Join(f.root, CarbonDirName, ManifestFilename)
	beforeManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	presentation, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: "layers"})
	if err != nil {
		t.Fatal(err)
	}
	if got := presentation.Clusters[f.cluster.ID]; got != (Icon{Kind: "builtin", Key: "layers"}) {
		t.Fatalf("cluster icon = %#v", got)
	}
	presentation, err = SetIcon(f.root, CatalogPresentationProject, f.project.ID, &Icon{Kind: "emoji", Key: "rocket"})
	if err != nil {
		t.Fatal(err)
	}
	if got := presentation.Projects[f.project.ID]; got != (Icon{Kind: "emoji", Key: "rocket"}) {
		t.Fatalf("project icon = %#v", got)
	}

	h, err := Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	presentation, err = h.CatalogPresentation()
	if err != nil {
		t.Fatal(err)
	}
	presentation.Clusters[f.cluster.ID] = Icon{Kind: "builtin", Key: "flask"}
	if reread, err := ListCatalogPresentation(f.root); err != nil || reread.Clusters[f.cluster.ID] != (Icon{Kind: "builtin", Key: "layers"}) {
		t.Fatalf("returned presentation was not defensive: %#v, %v", reread, err)
	}

	if _, err := h.SetIcon(CatalogPresentationCluster, f.cluster.ID, nil); err != nil {
		t.Fatal(err)
	}
	presentation, err = h.SetCatalogPresentationIcon(CatalogPresentationProject, f.project.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(presentation.Clusters) != 0 || len(presentation.Projects) != 0 {
		t.Fatalf("cleared presentation = %#v, want empty", presentation)
	}

	afterManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterManifest, beforeManifest) {
		t.Fatalf("catalog presentation modified home.json\nbefore=%s\nafter=%s", beforeManifest, afterManifest)
	}
	data, err := os.ReadFile(filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\n  \"version\": 1,\n  \"clusters\": {},\n  \"projects\": {}\n}\n"; got != want {
		t.Fatalf("presentation document = %q, want %q", got, want)
	}
}

func TestCatalogPresentationClearMissingIconDoesNotCreateDocument(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clearing a missing icon created document: %v", err)
	}
}

func TestCatalogPresentationRejectsUnsafeOrUnknownIconTokensWithoutMutation(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: "folder"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, icon := range []Icon{
		{Kind: "builtin", Key: "../folder"},
		{Kind: "builtin", Key: "https://example.test/icon.svg"},
		{Kind: "builtin", Key: "data:image/svg+xml;base64,PHN2Zz4="},
		{Kind: "builtin", Key: "<svg></svg>"},
		{Kind: "emoji", Key: "rocket.png"},
		{Kind: "emoji", Key: "8J+agA=="},
		{Kind: "url", Key: "https://example.test"},
		{Kind: "svg", Key: "folder"},
		{Kind: "builtin", Key: "Folder"},
		{Kind: "emoji", Key: "\U0001f680"},
	} {
		if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &icon); !errors.Is(err, ErrInvalidCatalogIcon) {
			t.Fatalf("SetCatalogPresentationIcon(%#v) = %v, want ErrInvalidCatalogIcon", icon, err)
		}
	}
	for _, request := range []struct {
		kind CatalogPresentationKind
		id   string
		want error
	}{
		{"clusters", f.cluster.ID, ErrInvalidCatalogPresentationTarget},
		{CatalogPresentationCluster, "cluster_" + strings.Repeat("g", 32), ErrInvalidCatalogPresentationTarget},
		{CatalogPresentationProject, "project_" + strings.Repeat("0", 32), ErrProjectNotFound},
		{CatalogPresentationCluster, "cluster_" + strings.Repeat("0", 32), ErrClusterNotFound},
	} {
		if _, err := SetCatalogPresentationIcon(f.root, request.kind, request.id, &Icon{Kind: "builtin", Key: "folder"}); !errors.Is(err, request.want) {
			t.Fatalf("SetCatalogPresentationIcon(%q, %q) = %v, want %v", request.kind, request.id, err, request.want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("invalid icon mutation changed document\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCatalogPresentationAcceptsOnlyPublishedTokenCatalog(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	for _, key := range []string{"folder", "layers", "monitor", "smartphone", "apple", "globe", "server", "package", "database", "flask"} {
		if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: key}); err != nil {
			t.Fatalf("builtin %q = %v", key, err)
		}
	}
	for _, key := range []string{"atom", "rocket", "spark", "puzzle", "shield", "palette"} {
		if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationProject, f.project.ID, &Icon{Kind: "emoji", Key: key}); err != nil {
			t.Fatalf("emoji %q = %v", key, err)
		}
	}
}

func TestCatalogPresentationFailsClosedForMalformedDocuments(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	path := filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)
	for _, test := range []struct {
		name string
		data []byte
		want error
	}{
		{
			name: "unknown top level field",
			data: []byte(`{"version":1,"clusters":{},"projects":{},"extra":true}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "duplicate top level key",
			data: []byte(`{"version":1,"clusters":{},"clusters":{},"projects":{}}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "duplicate nested key",
			data: []byte(`{"version":1,"clusters":{"` + f.cluster.ID + `":{"kind":"builtin","key":"folder","key":"layers"}},"projects":{}}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "unknown nested icon field",
			data: []byte(`{"version":1,"clusters":{"` + f.cluster.ID + `":{"kind":"builtin","key":"folder","html":"<b>x</b>"}},"projects":{}}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "unsafe icon path",
			data: []byte(`{"version":1,"clusters":{"` + f.cluster.ID + `":{"kind":"builtin","key":"../folder"}},"projects":{}}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "nil maps",
			data: []byte(`{"version":1,"clusters":null,"projects":{}}`),
			want: ErrInvalidCatalogPresentation,
		},
		{
			name: "future version",
			data: []byte(`{"version":2,"clusters":{},"projects":{}}`),
			want: ErrFutureCatalogPresentationVersion,
		},
		{
			name: "oversized",
			data: bytes.Repeat([]byte(" "), int(maxCatalogPresentationBytes+1)),
			want: ErrInvalidCatalogPresentation,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ListCatalogPresentation(f.root); !errors.Is(err, test.want) {
				t.Fatalf("ListCatalogPresentation = %v, want %v", err, test.want)
			}
			if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: "folder"}); !errors.Is(err, test.want) {
				t.Fatalf("SetCatalogPresentationIcon on malformed document = %v, want %v", err, test.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("malformed document was changed\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

func TestCatalogPresentationConcurrentMutationsDoNotLoseIcons(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	const workers = 24
	clusters := make([]Cluster, 0, workers)
	for i := 0; i < workers; i++ {
		cluster, err := CreateCluster(root, CreateClusterRequest{Name: "Target " + strconv.Itoa(i), Prefix: "C" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		clusters = append(clusters, cluster)
	}

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i, cluster := range clusters {
		icon := Icon{Kind: "builtin", Key: []string{"folder", "layers", "server", "database"}[i%4]}
		wg.Add(1)
		go func(clusterID string, icon Icon) {
			defer wg.Done()
			<-start
			_, err := SetCatalogPresentationIcon(root, CatalogPresentationCluster, clusterID, &icon)
			errs <- err
		}(cluster.ID, icon)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SetCatalogPresentationIcon = %v", err)
		}
	}
	presentation, err := ListCatalogPresentation(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(presentation.Clusters) != workers {
		t.Fatalf("concurrent cluster icon count = %d, want %d: %#v", len(presentation.Clusters), workers, presentation.Clusters)
	}
	for _, cluster := range clusters {
		if _, exists := presentation.Clusters[cluster.ID]; !exists {
			t.Fatalf("missing concurrently written icon for %s", cluster.ID)
		}
	}
}

func TestCatalogPresentationIsIndependentPerHome(t *testing.T) {
	useHomeTestCache(t)
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	first, err := CreateCluster(firstRoot, CreateClusterRequest{Name: "First", Prefix: "ONE"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateCluster(secondRoot, CreateClusterRequest{Name: "Second", Prefix: "TWO"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetCatalogPresentationIcon(firstRoot, CatalogPresentationCluster, first.ID, &Icon{Kind: "builtin", Key: "folder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetCatalogPresentationIcon(secondRoot, CatalogPresentationCluster, second.ID, &Icon{Kind: "emoji", Key: "atom"}); err != nil {
		t.Fatal(err)
	}
	firstPresentation, err := ListCatalogPresentation(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondPresentation, err := ListCatalogPresentation(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if firstPresentation.Clusters[first.ID] != (Icon{Kind: "builtin", Key: "folder"}) || len(firstPresentation.Clusters) != 1 {
		t.Fatalf("first home presentation = %#v", firstPresentation)
	}
	if secondPresentation.Clusters[second.ID] != (Icon{Kind: "emoji", Key: "atom"}) || len(secondPresentation.Clusters) != 1 {
		t.Fatalf("second home presentation = %#v", secondPresentation)
	}
}

func TestCatalogPresentationIsOrdinaryBackupVisibleFile(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: "folder"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || isReparsePoint(path, info) {
		t.Fatalf("catalog presentation must be a regular backup-visible file: %v", info.Mode())
	}
	repository, err := backup.NewRepository(backup.NewMemoryBlobStore(), "test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Create(context.Background(), backup.CreateOptions{
		SourceDir: filepath.Join(f.root, CarbonDirName), SourceID: "home-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := repository.Verify(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range manifest.Files {
		if entry.Path == CatalogPresentationFilename {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("backup snapshot omitted %s: %#v", CatalogPresentationFilename, manifest.Files)
	}
}

func TestCatalogPresentationRejectsReparseDocument(t *testing.T) {
	f := newCatalogPresentationFixture(t)
	target := filepath.Join(t.TempDir(), "presentation.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"clusters":{},"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, CarbonDirName, CatalogPresentationFilename)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ListCatalogPresentation(f.root); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("ListCatalogPresentation through symlink = %v, want ErrUnsafePath", err)
	}
	if _, err := SetCatalogPresentationIcon(f.root, CatalogPresentationCluster, f.cluster.ID, &Icon{Kind: "builtin", Key: "folder"}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("SetCatalogPresentationIcon through symlink = %v, want ErrUnsafePath", err)
	}
}
