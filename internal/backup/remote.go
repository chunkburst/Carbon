package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// UploadOptions makes remote publication opt-in. A zero-value options struct
// is deliberately disabled: creating a local snapshot never initiates network
// traffic or uploads data.
type UploadOptions struct {
	Enabled bool
}

// Upload verifies a local snapshot completely before copying it to remote. File
// objects are copied first and the manifest is copied last, so a remote never
// observes a published manifest whose referenced objects are still missing.
func (r *Repository) Upload(ctx context.Context, snapshot Snapshot, remote BlobStore, options UploadOptions) error {
	if !options.Enabled {
		return ErrRemoteDisabled
	}
	if remote == nil {
		return errors.New("backup remote upload has nil blob store")
	}
	manifest, err := r.Verify(ctx, snapshot)
	if err != nil {
		return err
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := ObjectKey(file.SHA256)
		data, err := r.getChecked(ctx, key, file.SHA256, file.Size)
		if err != nil {
			return fmt.Errorf("backup upload %q: %w", file.Path, err)
		}
		if err := putRemoteImmutable(ctx, remote, key, data, file.SHA256); err != nil {
			return fmt.Errorf("backup upload %q: %w", file.Path, err)
		}
	}
	manifestKey := ManifestKey(snapshot.ID)
	manifestData, err := r.getChecked(ctx, manifestKey, snapshot.ID, -1)
	if err != nil {
		return fmt.Errorf("backup upload manifest: %w", err)
	}
	if err := putRemoteImmutable(ctx, remote, manifestKey, manifestData, snapshot.ID); err != nil {
		return fmt.Errorf("backup upload manifest: %w", err)
	}
	// Publication is not considered successful until the remote object graph can
	// be read back and verified through the same store boundary. In particular,
	// callers that wrap a remote with EncryptedBlobStore verify decrypted
	// plaintext while the raw provider still contains only ciphertext.
	remoteRepository, err := NewRepository(remote, r.appVersion)
	if err != nil {
		return err
	}
	if _, err := remoteRepository.Verify(ctx, snapshot); err != nil {
		return fmt.Errorf("backup verify uploaded snapshot: %w", err)
	}
	return nil
}

func putRemoteImmutable(ctx context.Context, remote BlobStore, key string, data []byte, checksum string) error {
	info, created, err := remote.PutIfAbsent(ctx, key, data, PutOptions{SHA256: checksum})
	if err != nil {
		return err
	}
	if created {
		return checkInfo(key, data, info)
	}
	existing, existingInfo, err := remote.Get(ctx, key)
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
