package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestCreateVerifyRestoreLocal(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "nested", "keep.txt"), []byte("keep\n"), 0o640)
	writeTestFile(t, filepath.Join(source, "plain.txt"), []byte("plain\n"), 0o600)
	writeTestFile(t, filepath.Join(source, "write.lock"), []byte("lock"), 0o600)
	writeTestFile(t, filepath.Join(source, ".tmp-working"), []byte("temporary"), 0o600)
	writeTestFile(t, filepath.Join(source, "cache", "generated.txt"), []byte("cache"), 0o600)
	writeTestFile(t, filepath.Join(source, ".env"), []byte("TOKEN=must-not-back-up"), 0o600)
	writeTestFile(t, filepath.Join(source, "keys", "service.pem"), []byte("credential"), 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"), []byte("credential"), 0o600)
	writeTestFile(t, filepath.Join(source, ".cairn", "live", "heartbeat"), []byte("ephemeral"), 0o600)
	writeTestFile(t, filepath.Join(source, ".carbon", "live", "heartbeat"), []byte("ephemeral"), 0o600)
	writeTestFile(t, filepath.Join(source, ".carbon", "locks", "active"), []byte("ephemeral"), 0o600)
	writeTestFile(t, filepath.Join(source, ".carbon", "staging", "partial"), []byte("ephemeral"), 0o600)

	store, err := NewLocalBlobStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewRepository(store, "carbon-test")
	if err != nil {
		t.Fatal(err)
	}
	repo.now = func() time.Time { return time.Date(2026, 8, 5, 9, 10, 11, 123456000, time.UTC) }

	snapshot, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "project:stable-42"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSHA256(snapshot.ID); err != nil {
		t.Fatalf("snapshot ID: %v", err)
	}
	if snapshot.ManifestKey != ManifestKey(snapshot.ID) {
		t.Fatalf("manifest key = %q", snapshot.ManifestKey)
	}
	manifest, err := repo.Verify(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.AppVersion != "carbon-test" || manifest.SourceID != "project:stable-42" {
		t.Fatalf("unexpected manifest metadata: %+v", manifest)
	}
	if got, want := manifestPaths(manifest), []string{"nested/keep.txt", "plain.txt"}; !equalStrings(got, want) {
		t.Fatalf("manifest files = %v, want %v", got, want)
	}
	for _, entry := range manifest.Files {
		if entry.Size == 0 || entry.Mode == 0 {
			t.Fatalf("missing file metadata for %+v", entry)
		}
	}
	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Snapshot != snapshot {
		t.Fatalf("local snapshot list = (%+v, %v)", listed, err)
	}

	restoreRoot := t.TempDir()
	result, err := repo.Restore(ctx, snapshot, RestoreOptions{TempParent: restoreRoot, ApprovedRoot: restoreRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(result.StagingDir)
	if result.StagingDir == source {
		t.Fatal("restore wrote into source rather than a staging directory")
	}
	assertFileContents(t, filepath.Join(result.StagingDir, "nested", "keep.txt"), "keep\n")
	assertFileContents(t, filepath.Join(result.StagingDir, "plain.txt"), "plain\n")
	for _, excluded := range []string{"write.lock", ".tmp-working", filepath.Join("cache", "generated.txt"), ".env", filepath.Join("keys", "service.pem"), "credentials.json", filepath.Join(".cairn", "live", "heartbeat"), filepath.Join(".carbon", "live", "heartbeat"), filepath.Join(".carbon", "locks", "active"), filepath.Join(".carbon", "staging", "partial")} {
		if _, err := os.Stat(filepath.Join(result.StagingDir, excluded)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("excluded %q was restored (err=%v)", excluded, err)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(result.StagingDir, "nested", "keep.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
			t.Errorf("restored mode = %#o, want %#o", got, want)
		}
	}
}

func TestCreateIsIdempotentAndObjectsAreImmutable(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	data := []byte("immutable content")
	writeTestFile(t, filepath.Join(source, "file.txt"), data, 0o600)
	store := NewMemoryBlobStore()
	repo, err := NewRepository(store, "1.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	repo.now = func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }
	first, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same inputs produced different snapshots: %+v vs %+v", first, second)
	}
	_, _, err = store.PutIfAbsent(ctx, ObjectKey(SHA256Hex(data)), []byte("different"), PutOptions{})
	if !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("overwriting immutable object error = %v", err)
	}
}

func TestNewRepositoryUsesV100AsDefaultAppVersion(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("current product version"), 0o600)

	repository, err := NewRepository(NewMemoryBlobStore(), "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Create(context.Background(), CreateOptions{SourceDir: source, SourceID: "source-v100"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := repository.LoadManifest(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.AppVersion != "1.0.0" || DefaultAppVersion != "1.0.0" {
		t.Fatalf("default snapshot app version = %q (constant %q), want 1.0.0", manifest.AppVersion, DefaultAppVersion)
	}
}

func TestRestoreFailsClosedBeforeCreatingStagingDirectory(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("trusted"), 0o600)
	store := NewMemoryBlobStore()
	repo, _ := NewRepository(store, "test")
	snapshot, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source-1"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := repo.LoadManifest(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	broken := &corruptGetStore{BlobStore: store, key: ObjectKey(manifest.Files[0].SHA256)}
	brokenRepo, _ := NewRepository(broken, "test")
	restoreRoot := t.TempDir()
	staging := filepath.Join(restoreRoot, "must-not-exist")
	_, err = brokenRepo.Restore(ctx, snapshot, RestoreOptions{StagingDir: staging, ApprovedRoot: restoreRoot})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("restore error = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Lstat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restore created staging path before verification: %v", statErr)
	}
}

func TestEncryptedBlobStoreUsesVersionedUniqueEnvelope(t *testing.T) {
	ctx := context.Background()
	raw := NewMemoryBlobStore()
	key := bytes.Repeat([]byte{0x5a}, 32)
	provider := KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return bytes.Clone(key), nil
	})
	store, err := NewEncryptedBlobStore(raw, provider, "secret-manager://carbon-kek")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("private backup object")
	firstInfo, created, err := store.PutIfAbsent(ctx, "objects/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", payload, PutOptions{})
	if err != nil || !created {
		t.Fatalf("first encrypted put = (%+v, %v, %v)", firstInfo, created, err)
	}
	rawFirst, _, err := raw.Get(ctx, firstInfo.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(rawFirst, []byte(envelopeMagic)) || rawFirst[4] != envelopeVersion {
		t.Fatalf("missing versioned envelope header: %x", rawFirst[:min(len(rawFirst), 8)])
	}
	if bytes.Contains(rawFirst, payload) || bytes.Contains(rawFirst, key) || bytes.Contains(rawFirst, []byte("secret-manager://carbon-kek")) {
		t.Fatal("raw object leaked plaintext, master key, or key reference")
	}
	if _, created, err := store.PutIfAbsent(ctx, firstInfo.Key, payload, PutOptions{}); err != nil || created {
		t.Fatalf("idempotent encrypted put = (created=%v, err=%v)", created, err)
	}
	rawAgain, _, err := raw.Get(ctx, firstInfo.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawFirst, rawAgain) {
		t.Fatal("an existing encrypted object was overwritten")
	}
	secondKey := "objects/sha256/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := store.PutIfAbsent(ctx, secondKey, payload, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rawSecond, _, err := raw.Get(ctx, secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rawFirst, rawSecond) {
		t.Fatal("separate encrypted objects reused the same envelope bytes")
	}
	listed, err := store.List(ctx, "objects/")
	if err != nil || len(listed) != 2 || listed[0].SHA256 == "" || listed[1].SHA256 == "" {
		t.Fatalf("encrypted list = (%+v, %v)", listed, err)
	}
	got, info, err := store.Get(ctx, firstInfo.Key)
	if err != nil || !bytes.Equal(got, payload) || info.SHA256 != SHA256Hex(payload) {
		t.Fatalf("encrypted get = (%q, %+v, %v)", got, info, err)
	}
	wrong, _ := NewEncryptedBlobStore(raw, KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return bytes.Repeat([]byte{1}, 32), nil
	}), "secret-manager://other")
	if _, _, err := wrong.Get(ctx, firstInfo.Key); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong key error = %v", err)
	}
}

func TestEncryptedBlobStoreDecryptsLegacyEnvelope(t *testing.T) {
	ctx := context.Background()
	raw := NewMemoryBlobStore()
	key := bytes.Repeat([]byte{0x33}, 32)
	store, err := NewEncryptedBlobStore(raw, KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return bytes.Clone(key), nil
	}), "secret-manager://legacy-carbon-kek")
	if err != nil {
		t.Fatal(err)
	}
	objectKey := "objects/sha256/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	payload := []byte("legacy encrypted backup")
	legacyEnvelope, err := store.sealVersion(ctx, objectKey, payload, legacyEnvelopeVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := raw.PutIfAbsent(ctx, objectKey, legacyEnvelope, PutOptions{}); err != nil || !created {
		t.Fatalf("publish legacy envelope = created=%v err=%v", created, err)
	}
	got, _, err := store.Get(ctx, objectKey)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("decrypt legacy envelope = %q err=%v", got, err)
	}
}

func TestEncryptionProviderFailureDoesNotEchoSecretDiagnostics(t *testing.T) {
	store, err := NewEncryptedBlobStore(NewMemoryBlobStore(), KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return nil, errors.New("provider diagnostic includes secret-value")
	}), "vault://key")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.PutIfAbsent(context.Background(), "objects/failure", []byte("data"), PutOptions{})
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("provider error leaked through encryption layer: %v", err)
	}
}

func TestEncryptedBlobStoreConcurrentIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := NewEncryptedBlobStore(NewMemoryBlobStore(), KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return bytes.Repeat([]byte{8}, 32), nil
	}), "vault://key")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	created := make(chan bool, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, didCreate, err := store.PutIfAbsent(ctx, "objects/concurrent", []byte("same plaintext"), PutOptions{})
			if err != nil {
				errs <- err
				return
			}
			created <- didCreate
		}()
	}
	wg.Wait()
	close(errs)
	close(created)
	for err := range errs {
		t.Errorf("concurrent encrypted put: %v", err)
	}
	count := 0
	for didCreate := range created {
		if didCreate {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent encrypted created count = %d, want 1", count)
	}
}

func TestUploadIsExplicitAndRemoteSnapshotVerifies(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "one.txt"), []byte("one"), 0o600)
	local := NewMemoryBlobStore()
	repo, _ := NewRepository(local, "test")
	snapshot, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source-1"})
	if err != nil {
		t.Fatal(err)
	}
	remote := NewMemoryBlobStore()
	if err := repo.Upload(ctx, snapshot, remote, UploadOptions{}); !errors.Is(err, ErrRemoteDisabled) {
		t.Fatalf("disabled upload error = %v", err)
	}
	if _, _, err := remote.Get(ctx, snapshot.ManifestKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled upload wrote remote manifest: %v", err)
	}
	if err := repo.Upload(ctx, snapshot, remote, UploadOptions{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	remoteRepo, _ := NewRepository(remote, "test")
	if _, err := remoteRepo.Verify(ctx, snapshot); err != nil {
		t.Fatalf("remote snapshot verification: %v", err)
	}
	listed, err := remoteRepo.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Snapshot != snapshot || listed[0].Manifest.SourceID != "source-1" {
		t.Fatalf("remote snapshot list = (%+v, %v)", listed, err)
	}
}

func TestEncryptedRepositoryListsAndDecryptsManifests(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("encrypted snapshot"), 0o600)
	raw := NewMemoryBlobStore()
	store, err := NewEncryptedBlobStore(raw, KeyProviderFunc(func(context.Context, KeyReference) ([]byte, error) {
		return bytes.Repeat([]byte{7}, 32), nil
	}), "vault://carbon-key")
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := NewRepository(store, "test")
	snapshot, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "encrypted-source"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListSnapshots(ctx)
	if err != nil || len(items) != 1 || items[0].Snapshot != snapshot || items[0].Manifest.SourceID != "encrypted-source" {
		t.Fatalf("encrypted snapshot list = (%+v, %v)", items, err)
	}
	if _, err := repo.Verify(ctx, snapshot); err != nil {
		t.Fatalf("encrypted snapshot verification: %v", err)
	}
}

func TestListSnapshotsOrdersNewestFirst(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("one"), 0o600)
	repo, _ := NewRepository(NewMemoryBlobStore(), "test")
	repo.now = func() time.Time { return time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC) }
	older, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("two"), 0o600)
	repo.now = func() time.Time { return time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC) }
	newer, err := repo.Create(ctx, CreateOptions{SourceDir: source, SourceID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Snapshot != newer || items[1].Snapshot != older {
		t.Fatalf("snapshot order = %+v", items)
	}
}

func TestLocalBlobStoreRejectsTraversalAndChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalBlobStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutIfAbsent(ctx, "../escape", []byte("x"), PutOptions{}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, _, err := store.PutIfAbsent(ctx, "objects/sha256/test", []byte("x"), PutOptions{SHA256: strings.Repeat("0", 64)}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("checksum error = %v", err)
	}
	info, created, err := store.PutIfAbsent(ctx, "objects/sha256/test", []byte("x"), PutOptions{})
	if err != nil || !created {
		t.Fatalf("put = (%+v, %v, %v)", info, created, err)
	}
	if _, created, err := store.PutIfAbsent(ctx, "objects/sha256/test", []byte("x"), PutOptions{}); err != nil || created {
		t.Fatalf("idempotent local put = (created=%v, err=%v)", created, err)
	}
	if _, _, err := store.PutIfAbsent(ctx, "objects/sha256/test", []byte("not-x"), PutOptions{}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("immutable local put error = %v", err)
	}
}

func TestRetryRetriesOnlyMarkedTransientFailures(t *testing.T) {
	count := 0
	err := Retry(context.Background(), RetryPolicy{MaxAttempts: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond}, func(context.Context) error {
		count++
		if count < 3 {
			return testRetryableError{}
		}
		return nil
	})
	if err != nil || count != 3 {
		t.Fatalf("retry result = (%v, attempts=%d)", err, count)
	}
	count = 0
	err = Retry(context.Background(), RetryPolicy{MaxAttempts: 3, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond}, func(context.Context) error {
		count++
		return errors.New("permanent")
	})
	if err == nil || count != 1 {
		t.Fatalf("permanent retry result = (%v, attempts=%d)", err, count)
	}
}

func TestS3BlobStoreUsesConditionalPutChecksumAndIdempotency(t *testing.T) {
	ctx := context.Background()
	client := &fakeS3Client{objects: make(map[string]fakeS3Object)}
	store := &S3BlobStore{client: client, bucket: "bucket", prefix: "carbon", retry: RetryPolicy{MaxAttempts: 1}}
	data := []byte("s3 data")
	info, created, err := store.PutIfAbsent(ctx, "objects/sha256/test", data, PutOptions{})
	if err != nil || !created {
		t.Fatalf("first s3 put = (%+v, %v, %v)", info, created, err)
	}
	client.mu.Lock()
	object := client.objects["carbon/objects/sha256/test"]
	client.mu.Unlock()
	if object.metadata[s3ChecksumMetadataKey] != SHA256Hex(data) || object.ifNoneMatch != "*" {
		t.Fatalf("s3 conditional/checksum fields = %+v", object)
	}
	if _, created, err := store.PutIfAbsent(ctx, "objects/sha256/test", data, PutOptions{}); err != nil || created {
		t.Fatalf("idempotent s3 put = (created=%v, err=%v)", created, err)
	}
	if _, _, err := store.PutIfAbsent(ctx, "objects/sha256/test", []byte("other"), PutOptions{}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting s3 put error = %v", err)
	}
	got, gotInfo, err := store.Get(ctx, "objects/sha256/test")
	if err != nil || !bytes.Equal(got, data) || gotInfo.SHA256 != SHA256Hex(data) {
		t.Fatalf("s3 get = (%q, %+v, %v)", got, gotInfo, err)
	}
}

func TestS3ConfigurationUsesAWSAndCOSUsesDedicatedAdapter(t *testing.T) {
	credentials := testCredentialsProvider{}
	awsStore, err := NewS3BlobStore(S3Config{Bucket: "backup-bucket", Region: "us-east-1", Credentials: credentials})
	if err != nil {
		t.Fatal(err)
	}
	if awsStore.prefix != "" {
		t.Fatalf("unexpected AWS prefix %q", awsStore.prefix)
	}
	cosStore, err := NewCOSBlobStore(COSConfig{
		Bucket:      "bucket-1250000000",
		Region:      "ap-guangzhou",
		Endpoint:    "https://cos.ap-guangzhou.myqcloud.com",
		Prefix:      "carbon/carbon",
		Credentials: COSCredentials{SecretID: "test-id", SecretKey: "test-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cosStore.prefix != "carbon/carbon" {
		t.Fatalf("COS prefix = %q", cosStore.prefix)
	}
	if _, err := NewS3BlobStore(S3Config{Bucket: "backup-bucket", Region: "us-east-1", Credentials: credentials, Endpoint: "not a url"}); err == nil {
		t.Fatal("invalid endpoint was accepted")
	}
	if _, err := NewS3BlobStore(S3Config{Bucket: "backup-bucket", Region: "us-east-1", Credentials: credentials, Endpoint: "http://127.0.0.1:9000"}); err == nil {
		t.Fatal("insecure endpoint was accepted without explicit opt-in")
	}
	if _, err := NewS3BlobStore(S3Config{Bucket: "backup-bucket", Region: "us-east-1", Credentials: credentials, Endpoint: "http://198.51.100.1:9000", AllowInsecureEndpoint: true, UsePathStyle: true}); err == nil {
		t.Fatal("public insecure endpoint was accepted")
	}
	if _, err := NewS3BlobStore(S3Config{Bucket: "backup-bucket", Region: "us-east-1", Credentials: credentials, Endpoint: "http://127.0.0.1:9000", AllowInsecureEndpoint: true, UsePathStyle: true}); err != nil {
		t.Fatalf("explicit local insecure endpoint was rejected: %v", err)
	}
	if _, err := NewS3BlobStore(TencentCOSConfig("bucket-1250000000", "ap-guangzhou", "https://cos.ap-guangzhou.myqcloud.com", credentials)); err == nil {
		t.Fatal("S3 adapter accepted a COS endpoint instead of requiring the dedicated adapter")
	}
}

func TestS3ConstructorValidatesBucketAndRegion(t *testing.T) {
	credentials := testCredentialsProvider{}
	for name, config := range map[string]S3Config{
		"IP address bucket": {
			Bucket: "127.0.0.1", Region: "us-east-1", Credentials: credentials,
		},
		"uppercase bucket": {
			Bucket: "Carbon-Backup", Region: "us-east-1", Credentials: credentials,
		},
		"short bucket": {
			Bucket: "ab", Region: "us-east-1", Credentials: credentials,
		},
		"invalid region": {
			Bucket: "carbon-backup", Region: "us_east_1", Credentials: credentials,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3BlobStore(config); err == nil {
				t.Fatalf("invalid direct S3 config was accepted: %+v", config)
			}
		})
	}
	if _, err := NewS3BlobStore(S3Config{Bucket: "carbon-backup", Region: "us-east-1", Credentials: credentials}); err != nil {
		t.Fatalf("valid direct S3 config rejected: %v", err)
	}
}

func TestS3ListFollowsContinuationTokens(t *testing.T) {
	ctx := context.Background()
	client := &fakeS3Client{objects: map[string]fakeS3Object{
		"carbon/objects/a": {data: []byte("a")},
		"carbon/objects/b": {data: []byte("b")},
		"carbon/objects/c": {data: []byte("c")},
	}, pageSize: 2}
	store := &S3BlobStore{client: client, bucket: "bucket", prefix: "carbon", retry: RetryPolicy{MaxAttempts: 1}}
	items, err := store.List(ctx, "objects/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("s3 paged list count = %d, want 3", len(items))
	}
	if got := []string{items[0].Key, items[1].Key, items[2].Key}; !equalStrings(got, []string{"objects/a", "objects/b", "objects/c"}) {
		t.Fatalf("s3 paged list = %v", got)
	}
	if client.listCalls != 2 {
		t.Fatalf("s3 list calls = %d, want 2", client.listCalls)
	}
}

func TestS3ConditionalConflictRetries(t *testing.T) {
	ctx := context.Background()
	client := &fakeS3Client{
		objects:   make(map[string]fakeS3Object),
		putErrors: []error{testHTTPError{status: 409, message: "conditional conflict"}},
	}
	store := &S3BlobStore{client: client, bucket: "bucket", retry: RetryPolicy{MaxAttempts: 2, InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}
	if _, created, err := store.PutIfAbsent(ctx, "objects/retry", []byte("retry"), PutOptions{}); err != nil || !created {
		t.Fatalf("s3 conditional retry = (created=%v, err=%v)", created, err)
	}
	if client.putCalls != 2 {
		t.Fatalf("s3 put calls = %d, want 2", client.putCalls)
	}
}

func TestS3ReadsLegacyChecksumMetadata(t *testing.T) {
	ctx := context.Background()
	data := []byte("legacy")
	client := &fakeS3Client{objects: map[string]fakeS3Object{
		"objects/legacy": {data: data, metadata: map[string]string{legacyS3ChecksumMetadataKey: SHA256Hex(data)}},
	}}
	store := &S3BlobStore{client: client, bucket: "bucket", retry: RetryPolicy{MaxAttempts: 1}}
	got, _, err := store.Get(ctx, "objects/legacy")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("legacy metadata get = (%q, %v)", got, err)
	}
}

func TestCOSBlobStoreUsesVirtualHostForbidOverwriteAndBodyChecksums(t *testing.T) {
	provider := &fakeCOSProvider{objects: make(map[string][]byte)}
	server := httptest.NewServer(provider)
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	const bucket = "carbon-backup-1250000000"
	store, err := NewCOSBlobStore(COSConfig{
		Bucket:   bucket,
		Prefix:   "carbon/remote",
		Region:   "ap-guangzhou",
		Endpoint: "https://cos.ap-guangzhou.myqcloud.com",
		Credentials: COSCredentials{
			SecretID: "test-id", SecretKey: "test-key",
		},
		HTTPClient: &http.Client{Transport: cosRewriteTransport{target: target}},
		Retry:      RetryPolicy{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.wantHost = bucket + ".cos.ap-guangzhou.myqcloud.com"
	key := "objects/sha256/" + strings.Repeat("a", 64)
	payload := []byte("COS immutable body")
	info, created, err := store.PutIfAbsent(context.Background(), key, payload, PutOptions{})
	if err != nil || !created || info.SHA256 != SHA256Hex(payload) {
		t.Fatalf("COS first put = (%+v, %v, %v)", info, created, err)
	}
	if _, created, err := store.PutIfAbsent(context.Background(), key, payload, PutOptions{}); err != nil || created {
		t.Fatalf("COS idempotent put = (created=%v, err=%v)", created, err)
	}
	if _, _, err := store.PutIfAbsent(context.Background(), key, []byte("other"), PutOptions{}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("COS immutable conflict = %v", err)
	}
	got, gotInfo, err := store.Get(context.Background(), key)
	if err != nil || !bytes.Equal(got, payload) || gotInfo.SHA256 != SHA256Hex(payload) {
		t.Fatalf("COS get = (%q, %+v, %v)", got, gotInfo, err)
	}
	if stat, err := store.Stat(context.Background(), key); err != nil || stat.SHA256 != SHA256Hex(payload) || stat.Size != int64(len(payload)) {
		t.Fatalf("COS stat = (%+v, %v)", stat, err)
	}
	listed, err := store.List(context.Background(), "objects/")
	if err != nil || len(listed) != 1 || listed[0].Key != key || listed[0].Size != int64(len(payload)) {
		t.Fatalf("COS list = (%+v, %v)", listed, err)
	}
	if provider.badHost || !provider.forbidOverwrite || !provider.validBodyChecksum {
		t.Fatalf("COS request contract: host=%v forbid=%v checksum=%v", provider.badHost, provider.forbidOverwrite, provider.validBodyChecksum)
	}
}

func TestLocalBlobStoreRejectsSymlinkedParentChain(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(parent, "redirect")
	if err := createBackupTestDirectoryLink(link, outside); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewLocalBlobStore(filepath.Join(link, "store")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("symlinked local-store parent error = %v", err)
	}
}

func TestRestoreRequiresApprovedRootAndConfinesStaging(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("restore"), 0o600)
	repository, _ := NewRepository(NewMemoryBlobStore(), "test")
	snapshot, err := repository.Create(context.Background(), CreateOptions{SourceDir: source, SourceID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	approvedRoot := t.TempDir()
	result, err := repository.RestoreToStaging(context.Background(), snapshot, RestoreOptions{ApprovedRoot: approvedRoot})
	if err != nil {
		t.Fatalf("restore under approved root: %v", err)
	}
	defer os.RemoveAll(result.StagingDir)
	if relative, err := filepath.Rel(approvedRoot, result.StagingDir); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatalf("default staging directory escaped approved root: root=%q staging=%q rel=%q err=%v", approvedRoot, result.StagingDir, relative, err)
	}

	externalRoot := t.TempDir()
	if _, err := repository.Restore(context.Background(), snapshot, RestoreOptions{TempParent: approvedRoot}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Restore without an approved root = %v, want ErrUnsafePath", err)
	}
	for name, options := range map[string]RestoreOptions{
		"external temp parent": {ApprovedRoot: approvedRoot, TempParent: externalRoot},
		"external staging dir": {ApprovedRoot: approvedRoot, StagingDir: filepath.Join(externalRoot, "stage")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.RestoreToStaging(context.Background(), snapshot, options); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("restore option error = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestRestoreRejectsSymlinkedStagingOrTempParent(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "file.txt"), []byte("restore"), 0o600)
	repository, _ := NewRepository(NewMemoryBlobStore(), "test")
	snapshot, err := repository.Create(context.Background(), CreateOptions{SourceDir: source, SourceID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(parent, "redirect")
	if err := createBackupTestDirectoryLink(link, outside); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for name, options := range map[string]RestoreOptions{
		"staging parent": {ApprovedRoot: parent, StagingDir: filepath.Join(link, "stage")},
		"temp parent":    {ApprovedRoot: parent, TempParent: link},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.RestoreToStaging(context.Background(), snapshot, options); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("symlinked restore parent error = %v", err)
			}
		})
	}
}

type cosRewriteTransport struct{ target *url.URL }

func (t cosRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	originalHost := request.URL.Host
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = originalHost
	return http.DefaultTransport.RoundTrip(clone)
}

type fakeCOSProvider struct {
	mu                sync.Mutex
	objects           map[string][]byte
	wantHost          string
	badHost           bool
	forbidOverwrite   bool
	validBodyChecksum bool
}

func (s *fakeCOSProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wantHost != "" && r.Host != s.wantHost {
		s.badHost = true
	}
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		prefix := r.URL.Query().Get("prefix")
		keys := make([]string, 0)
		for key := range s.objects {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		var body strings.Builder
		body.WriteString("<ListBucketResult><Name>test</Name><Prefix>" + prefix + "</Prefix><IsTruncated>false</IsTruncated>")
		for _, key := range keys {
			body.WriteString(fmt.Sprintf("<Contents><Key>%s</Key><Size>%d</Size></Contents>", key, len(s.objects[key])))
		}
		body.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, body.String())
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		if _, exists := s.objects[key]; exists && r.Header.Get("X-Cos-Forbid-Overwrite") == "true" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		s.forbidOverwrite = r.Header.Get("X-Cos-Forbid-Overwrite") == "true"
		s.validBodyChecksum = r.Header.Get("X-Cos-Content-Sha1") == sha1Hex(data)
		s.objects[key] = bytes.Clone(data)
		w.Header().Set("X-Cos-Hash-Crc64ecma", fmt.Sprintf("%d", crc64.Checksum(data, crc64.MakeTable(crc64.ECMA))))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, exists := s.objects[key]
		if !exists {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Header().Set("X-Cos-Content-Sha1", sha1Hex(data))
		_, _ = w.Write(data)
	case http.MethodHead:
		data, exists := s.objects[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

type corruptGetStore struct {
	BlobStore
	key string
}

func (s *corruptGetStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if key == s.key {
		data := []byte("corrupted")
		return data, blobInfo(key, data), nil
	}
	return s.BlobStore.Get(ctx, key)
}

type testRetryableError struct{}

func (testRetryableError) Error() string   { return "retryable" }
func (testRetryableError) Retryable() bool { return true }

type testCredentialsProvider struct{}

func (testCredentialsProvider) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
}

type fakeS3Object struct {
	data        []byte
	metadata    map[string]string
	ifNoneMatch string
}

type fakeS3Client struct {
	mu        sync.Mutex
	objects   map[string]fakeS3Object
	pageSize  int
	listCalls int
	putErrors []error
	putCalls  int
}

func (s *fakeS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	key := aws.ToString(input.Key)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putCalls++
	if len(s.putErrors) > 0 {
		err := s.putErrors[0]
		s.putErrors = s.putErrors[1:]
		return nil, err
	}
	if _, exists := s.objects[key]; exists && aws.ToString(input.IfNoneMatch) == "*" {
		return nil, testHTTPError{status: 412, message: "precondition failed"}
	}
	metadata := make(map[string]string, len(input.Metadata))
	for name, value := range input.Metadata {
		metadata[name] = value
	}
	s.objects[key] = fakeS3Object{data: bytes.Clone(data), metadata: metadata, ifNoneMatch: aws.ToString(input.IfNoneMatch)}
	return &s3.PutObjectOutput{}, nil
}

func (s *fakeS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	s.mu.Lock()
	object, exists := s.objects[aws.ToString(input.Key)]
	s.mu.Unlock()
	if !exists {
		return nil, testHTTPError{status: 404, message: "not found"}
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(object.data)),
		ContentLength: aws.Int64(int64(len(object.data))),
		Metadata:      object.metadata,
	}, nil
}

func (s *fakeS3Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	s.mu.Lock()
	object, exists := s.objects[aws.ToString(input.Key)]
	s.mu.Unlock()
	if !exists {
		return nil, testHTTPError{status: 404, message: "not found"}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(object.data))), Metadata: object.metadata}, nil
}

func (s *fakeS3Client) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, aws.ToString(input.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if token := aws.ToString(input.ContinuationToken); token != "" {
		if _, err := fmt.Sscanf(token, "%d", &start); err != nil || start < 0 || start > len(keys) {
			s.mu.Unlock()
			return nil, fmt.Errorf("bad continuation token")
		}
	}
	pageSize := s.pageSize
	if pageSize <= 0 {
		pageSize = len(keys)
	}
	end := start + pageSize
	if end > len(keys) {
		end = len(keys)
	}
	objects := make([]s3types.Object, 0, end-start)
	for _, key := range keys[start:end] {
		object := s.objects[key]
		objects = append(objects, s3types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(object.data)))})
	}
	s.listCalls++
	s.mu.Unlock()
	output := &s3.ListObjectsV2Output{Contents: objects}
	if end < len(keys) {
		output.IsTruncated = aws.Bool(true)
		output.NextContinuationToken = aws.String(fmt.Sprintf("%d", end))
	}
	return output, nil
}

type testHTTPError struct {
	status  int
	message string
}

func (e testHTTPError) Error() string       { return e.message }
func (e testHTTPError) HTTPStatusCode() int { return e.status }

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func manifestPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		paths = append(paths, entry.Path)
	}
	sort.Strings(paths)
	return paths
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func createBackupTestDirectoryLink(link, target string) error {
	if err := os.Symlink(target, link); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	// Developer Mode / SeCreateSymbolicLinkPrivilege is often unavailable on
	// Windows CI. A directory junction is still a reparse point and exercises
	// the exact parent-chain defense without elevated privileges.
	return exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).Run()
}

func ExampleNewCOSBlobStore() {
	store, err := NewCOSBlobStore(COSConfig{
		Bucket:      "example-bucket-1250000000",
		Region:      "ap-guangzhou",
		Endpoint:    "https://cos.ap-guangzhou.myqcloud.com",
		Credentials: COSCredentials{SecretID: "from-env", SecretKey: "from-env"},
	})
	fmt.Println(err == nil && store != nil)
	// Output: true
}
