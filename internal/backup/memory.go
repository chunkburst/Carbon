package backup

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MemoryBlobStore is an in-memory BlobStore intended for tests, local tooling,
// and callers that provide their own persistence boundary.
type MemoryBlobStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

// NewMemoryBlobStore returns an empty immutable in-memory store.
func NewMemoryBlobStore() *MemoryBlobStore {
	return &MemoryBlobStore{objects: make(map[string][]byte)}
}

func (s *MemoryBlobStore) PutIfAbsent(ctx context.Context, key string, data []byte, opts PutOptions) (BlobInfo, bool, error) {
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

	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.objects[key]; ok {
		if !bytes.Equal(current, data) {
			return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
		}
		return BlobInfo{Key: key, Size: int64(len(current)), SHA256: checksum}, false, nil
	}
	s.objects[key] = bytes.Clone(data)
	return BlobInfo{Key: key, Size: int64(len(data)), SHA256: checksum}, true, nil
}

func (s *MemoryBlobStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	s.mu.RLock()
	data, ok := s.objects[key]
	if ok {
		data = bytes.Clone(data)
	}
	s.mu.RUnlock()
	if !ok {
		return nil, BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return data, blobInfo(key, data), nil
}

func (s *MemoryBlobStore) Stat(ctx context.Context, key string) (BlobInfo, error) {
	_, info, err := s.Get(ctx, key)
	return info, err
}

func (s *MemoryBlobStore) List(ctx context.Context, prefix string) ([]BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBlobPrefix(prefix); err != nil {
		return nil, err
	}
	s.mu.RLock()
	keys := make([]string, 0)
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	infos := make([]BlobInfo, 0, len(keys))
	for _, key := range keys {
		infos = append(infos, blobInfo(key, s.objects[key]))
	}
	s.mu.RUnlock()
	return infos, nil
}
