package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrInvalidKeyMaterial indicates that a provider did not supply an AES-256 key.
	ErrInvalidKeyMaterial = errors.New("invalid backup encryption key material")
	// ErrInvalidEnvelope indicates an object that is not a supported authenticated envelope.
	ErrInvalidEnvelope = errors.New("invalid backup encryption envelope")
)

// KeyReference is an opaque identifier resolved outside this package (for
// example by an OS keychain, secret manager, or injected workload identity).
// It is kept only in process memory and is never written into an object header
// or manifest.
type KeyReference string

// KeyProvider resolves a 32-byte AES-256 wrapping key from an external source.
// Implementations must not write returned key material to repository files,
// configuration, logs, command-line arguments, or error strings.
type KeyProvider interface {
	Resolve(ctx context.Context, reference KeyReference) ([]byte, error)
}

// KeyProviderFunc adapts a function into a KeyProvider.
type KeyProviderFunc func(context.Context, KeyReference) ([]byte, error)

func (f KeyProviderFunc) Resolve(ctx context.Context, reference KeyReference) ([]byte, error) {
	return f(ctx, reference)
}

// EncryptedBlobStore encrypts every object client-side before passing it to its
// wrapped store. It uses a fresh random data-encryption key and two fresh GCM
// nonces per written object: one to encrypt the object and one to wrap that
// data key under the externally resolved AES-256 master key.
type EncryptedBlobStore struct {
	store     BlobStore
	keys      KeyProvider
	reference KeyReference
	random    io.Reader
}

// NewEncryptedBlobStore returns a BlobStore whose wrapped backend only sees
// versioned authenticated ciphertext. It does not persist the key reference or
// resolve a key until an operation needs one.
func NewEncryptedBlobStore(store BlobStore, keys KeyProvider, reference KeyReference) (*EncryptedBlobStore, error) {
	if store == nil {
		return nil, errors.New("backup encrypted store has nil backend")
	}
	if keys == nil {
		return nil, errors.New("backup encrypted store has nil key provider")
	}
	if reference == "" {
		return nil, errors.New("backup encrypted store key reference is empty")
	}
	return &EncryptedBlobStore{store: store, keys: keys, reference: reference, random: rand.Reader}, nil
}

func (s *EncryptedBlobStore) PutIfAbsent(ctx context.Context, key string, data []byte, opts PutOptions) (BlobInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, false, err
	}
	if err := validateBlobKey(key); err != nil {
		return BlobInfo{}, false, err
	}
	plainChecksum, err := validatePut(data, opts)
	if err != nil {
		return BlobInfo{}, false, err
	}
	// Encryption is intentionally randomized, so encrypting the same plaintext
	// twice produces different ciphertext. Check an existing logical object
	// before sealing a new one; otherwise an immutable raw backend would quite
	// correctly reject a different ciphertext for the same key.
	if info, found, err := s.existingPlaintext(ctx, key, data, plainChecksum); err != nil {
		return BlobInfo{}, false, err
	} else if found {
		return info, false, nil
	}
	ciphertext, err := s.seal(ctx, key, data)
	if err != nil {
		return BlobInfo{}, false, err
	}
	defer wipe(ciphertext)

	_, created, err := s.store.PutIfAbsent(ctx, key, ciphertext, PutOptions{SHA256: SHA256Hex(ciphertext)})
	if err != nil {
		// A second writer can publish its valid randomized envelope after our
		// initial absence check. Resolve that raw conflict at the logical
		// plaintext layer, never by overwriting the ciphertext.
		if errors.Is(err, ErrImmutableConflict) {
			if info, found, existingErr := s.existingPlaintext(ctx, key, data, plainChecksum); existingErr != nil {
				return BlobInfo{}, false, existingErr
			} else if found {
				return info, false, nil
			}
		}
		return BlobInfo{}, false, err
	}
	if !created {
		if info, found, err := s.existingPlaintext(ctx, key, data, plainChecksum); err != nil {
			return BlobInfo{}, false, err
		} else if found {
			return info, false, nil
		}
		return BlobInfo{}, false, fmt.Errorf("%w: object disappeared after create-only write", ErrImmutableConflict)
	}
	return BlobInfo{Key: key, Size: int64(len(data)), SHA256: plainChecksum}, created, nil
}

func (s *EncryptedBlobStore) existingPlaintext(ctx context.Context, key string, want []byte, checksum string) (BlobInfo, bool, error) {
	current, _, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return BlobInfo{}, false, nil
	}
	if err != nil {
		return BlobInfo{}, false, err
	}
	if !bytes.Equal(current, want) {
		return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
	}
	return BlobInfo{Key: key, Size: int64(len(want)), SHA256: checksum}, true, nil
}

func (s *EncryptedBlobStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	ciphertext, info, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	if info.SHA256 != "" && SHA256Hex(ciphertext) != info.SHA256 {
		return nil, BlobInfo{}, fmt.Errorf("%w", ErrChecksumMismatch)
	}
	plaintext, err := s.open(ctx, key, ciphertext)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	return plaintext, blobInfo(key, plaintext), nil
}

func (s *EncryptedBlobStore) Stat(ctx context.Context, key string) (BlobInfo, error) {
	_, info, err := s.Get(ctx, key)
	return info, err
}

func (s *EncryptedBlobStore) List(ctx context.Context, prefix string) ([]BlobInfo, error) {
	lister, ok := s.store.(BlobLister)
	if !ok {
		return nil, ErrListingUnsupported
	}
	items, err := lister.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	infos := make([]BlobInfo, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, info, err := s.Get(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

const (
	envelopeMagic         = "CBEN"
	envelopeVersion       = byte(2)
	legacyEnvelopeVersion = byte(1)
	envelopeFixed         = 9 // magic(4), version(1), nonce sizes(2), wrapped-key size(2)
)

func (s *EncryptedBlobStore) seal(ctx context.Context, key string, plaintext []byte) ([]byte, error) {
	return s.sealVersion(ctx, key, plaintext, envelopeVersion)
}

func (s *EncryptedBlobStore) sealVersion(ctx context.Context, key string, plaintext []byte, version byte) ([]byte, error) {
	if version != envelopeVersion && version != legacyEnvelopeVersion {
		return nil, fmt.Errorf("%w: unsupported envelope version", ErrInvalidEnvelope)
	}
	masterKey, err := s.resolveMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	defer wipe(masterKey)
	wrapping, err := newGCM(masterKey)
	if err != nil {
		return nil, err
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(s.random, dataKey); err != nil {
		return nil, fmt.Errorf("backup encryption random data key: %w", err)
	}
	defer wipe(dataKey)
	payload, err := newGCM(dataKey)
	if err != nil {
		return nil, err
	}
	wrapNonce, err := randomNonce(s.random, wrapping)
	if err != nil {
		return nil, err
	}
	payloadNonce, err := randomNonce(s.random, payload)
	if err != nil {
		return nil, err
	}
	wrappedKey := wrapping.Seal(nil, wrapNonce, dataKey, envelopeAAD(version, key, "key"))
	ciphertext := payload.Seal(nil, payloadNonce, plaintext, envelopeAAD(version, key, "payload"))
	if len(wrappedKey) > 0xffff {
		return nil, fmt.Errorf("%w: wrapped key is too large", ErrInvalidEnvelope)
	}

	envelope := make([]byte, 0, envelopeFixed+len(wrapNonce)+len(payloadNonce)+len(wrappedKey)+len(ciphertext))
	envelope = append(envelope, envelopeMagic...)
	envelope = append(envelope, version, byte(len(wrapNonce)), byte(len(payloadNonce)))
	var wrappedSize [2]byte
	binary.BigEndian.PutUint16(wrappedSize[:], uint16(len(wrappedKey)))
	envelope = append(envelope, wrappedSize[:]...)
	envelope = append(envelope, wrapNonce...)
	envelope = append(envelope, payloadNonce...)
	envelope = append(envelope, wrappedKey...)
	envelope = append(envelope, ciphertext...)
	return envelope, nil
}

func (s *EncryptedBlobStore) open(ctx context.Context, key string, envelope []byte) ([]byte, error) {
	version, wrapNonce, payloadNonce, wrappedKey, ciphertext, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	masterKey, err := s.resolveMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	defer wipe(masterKey)
	wrapping, err := newGCM(masterKey)
	if err != nil {
		return nil, err
	}
	if len(wrapNonce) != wrapping.NonceSize() {
		return nil, fmt.Errorf("%w: unsupported wrapping nonce size", ErrInvalidEnvelope)
	}
	dataKey, err := wrapping.Open(nil, wrapNonce, wrappedKey, envelopeAAD(version, key, "key"))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot authenticate wrapped key", ErrInvalidEnvelope)
	}
	defer wipe(dataKey)
	payload, err := newGCM(dataKey)
	if err != nil {
		return nil, err
	}
	if len(payloadNonce) != payload.NonceSize() {
		return nil, fmt.Errorf("%w: unsupported payload nonce size", ErrInvalidEnvelope)
	}
	plaintext, err := payload.Open(nil, payloadNonce, ciphertext, envelopeAAD(version, key, "payload"))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot authenticate payload", ErrInvalidEnvelope)
	}
	return plaintext, nil
}

func (s *EncryptedBlobStore) resolveMasterKey(ctx context.Context) ([]byte, error) {
	provided, err := s.keys.Resolve(ctx, s.reference)
	if err != nil {
		// Do not propagate a provider's diagnostic verbatim: an external secret
		// backend might include a sensitive identifier in it and callers often log
		// returned errors.
		return nil, errors.New("backup encryption key resolution failed")
	}
	if len(provided) != 32 {
		return nil, fmt.Errorf("%w: need 32 bytes", ErrInvalidKeyMaterial)
	}
	return bytes.Clone(provided), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: need 32 bytes", ErrInvalidKeyMaterial)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyMaterial, err)
	}
	return cipher.NewGCM(block)
}

func randomNonce(random io.Reader, aead cipher.AEAD) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("backup encryption random nonce: %w", err)
	}
	return nonce, nil
}

func parseEnvelope(envelope []byte) (version byte, wrapNonce, payloadNonce, wrappedKey, ciphertext []byte, err error) {
	if len(envelope) < envelopeFixed || string(envelope[:4]) != envelopeMagic || (envelope[4] != envelopeVersion && envelope[4] != legacyEnvelopeVersion) {
		return 0, nil, nil, nil, nil, fmt.Errorf("%w: unknown header", ErrInvalidEnvelope)
	}
	version = envelope[4]
	wrapSize := int(envelope[5])
	payloadSize := int(envelope[6])
	wrappedSize := int(binary.BigEndian.Uint16(envelope[7:9]))
	start := envelopeFixed
	endWrapNonce := start + wrapSize
	endPayloadNonce := endWrapNonce + payloadSize
	endWrappedKey := endPayloadNonce + wrappedSize
	if wrapSize == 0 || payloadSize == 0 || wrappedSize == 0 || endWrapNonce < start || endPayloadNonce < endWrapNonce || endWrappedKey < endPayloadNonce || endWrappedKey >= len(envelope) {
		return 0, nil, nil, nil, nil, fmt.Errorf("%w: malformed header lengths", ErrInvalidEnvelope)
	}
	return version, envelope[start:endWrapNonce], envelope[endWrapNonce:endPayloadNonce], envelope[endPayloadNonce:endWrappedKey], envelope[endWrappedKey:], nil
}

func envelopeAAD(version byte, key, purpose string) []byte {
	if version == legacyEnvelopeVersion {
		return []byte("cairn-carbon-envelope-v1\x00" + purpose + "\x00" + key)
	}
	return []byte("carbon-envelope-v2\x00" + purpose + "\x00" + key)
}

func wipe(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
