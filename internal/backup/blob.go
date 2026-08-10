package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	// ErrNotFound indicates that a blob key does not exist.
	ErrNotFound = errors.New("backup blob not found")
	// ErrInvalidKey indicates a blob key that cannot be mapped safely to a store.
	ErrInvalidKey = errors.New("invalid backup blob key")
	// ErrChecksumMismatch indicates corrupt or unexpected object content.
	ErrChecksumMismatch = errors.New("backup checksum mismatch")
	// ErrImmutableConflict indicates an existing immutable key has different data.
	ErrImmutableConflict = errors.New("backup immutable object conflict")
	// ErrRemoteDisabled prevents a caller from treating a non-upload as success.
	ErrRemoteDisabled = errors.New("backup remote upload is not explicitly enabled")
	// ErrListingUnsupported indicates a store that cannot enumerate immutable keys.
	ErrListingUnsupported = errors.New("backup blob listing is not supported")
)

// BlobInfo describes bytes held by a BlobStore. SHA256 is lowercase hexadecimal
// and is always for the bytes visible through that store (plaintext for an
// EncryptedBlobStore, ciphertext for its wrapped store).
type BlobInfo struct {
	Key    string
	Size   int64
	SHA256 string
}

// PutOptions carries integrity information for PutIfAbsent. When SHA256 is
// present, a store must reject bytes which do not match it.
type PutOptions struct {
	SHA256 string
}

// BlobStore is the immutable object-store boundary used by snapshots. Stores
// must atomically make a key visible only once and must never overwrite it.
//
// PutIfAbsent returns created=false for an already-present object with the same
// bytes. An implementation must return ErrImmutableConflict when those bytes
// differ. Get returns a defensive copy when its backing implementation keeps
// objects in memory.
type BlobStore interface {
	PutIfAbsent(ctx context.Context, key string, data []byte, opts PutOptions) (info BlobInfo, created bool, err error)
	Get(ctx context.Context, key string) (data []byte, info BlobInfo, err error)
	Stat(ctx context.Context, key string) (info BlobInfo, err error)
}

// BlobLister is an optional capability implemented by stores that can enumerate
// immutable keys. Prefix uses slash-separated object-key syntax and may be
// empty. Repository.List requires this capability; Create, Verify, and Restore
// do not.
type BlobLister interface {
	List(ctx context.Context, prefix string) ([]BlobInfo, error)
}

// SHA256Hex returns the canonical checksum used throughout the backup format.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func verifyBytes(data []byte, want string) error {
	if err := validateSHA256(want); err != nil {
		return err
	}
	if SHA256Hex(data) != want {
		return fmt.Errorf("%w", ErrChecksumMismatch)
	}
	return nil
}

func validatePut(data []byte, opts PutOptions) (string, error) {
	checksum := SHA256Hex(data)
	if opts.SHA256 == "" {
		return checksum, nil
	}
	if err := validateSHA256(opts.SHA256); err != nil {
		return "", err
	}
	if checksum != opts.SHA256 {
		return "", fmt.Errorf("%w", ErrChecksumMismatch)
	}
	return checksum, nil
}

func validateSHA256(value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("%w: sha256 must be lowercase hexadecimal", ErrChecksumMismatch)
	}
	if strings.ToLower(value) != value {
		return fmt.Errorf("%w: sha256 must be lowercase hexadecimal", ErrChecksumMismatch)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%w: sha256 must be hexadecimal", ErrChecksumMismatch)
	}
	return nil
}

func validateBlobKey(key string) error {
	if key == "" || len(key) > 1024 || strings.ContainsRune(key, 0) || strings.Contains(key, `\`) {
		return fmt.Errorf("%w: malformed key", ErrInvalidKey)
	}
	if strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return fmt.Errorf("%w: malformed key", ErrInvalidKey)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: malformed key", ErrInvalidKey)
		}
	}
	return nil
}

func validateBlobPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return fmt.Errorf("%w: malformed prefix", ErrInvalidKey)
	}
	return validateBlobKey(prefix)
}

func blobInfo(key string, data []byte) BlobInfo {
	return BlobInfo{Key: key, Size: int64(len(data)), SHA256: SHA256Hex(data)}
}
