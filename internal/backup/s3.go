package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	s3ChecksumMetadataKey       = "carbon-sha256"
	legacyS3ChecksumMetadataKey = "cairn-sha256"
)

// S3Config configures an AWS S3 remote. Endpoint is optional for AWS S3. A
// custom endpoint is accepted only for an official AWS endpoint or a clearly
// local/private development endpoint with explicit insecure-dev opt-in; this
// prevents an injected cloud credential provider from being sent to an
// arbitrary HTTPS host. Tencent COS uses COSConfig and COSBlobStore instead.
//
// Credentials is an in-memory provider supplied by the integration (for
// example from IAM, an OS keychain, or a secret manager). This package neither
// loads nor persists credential strings.
type S3Config struct {
	Bucket       string
	Prefix       string
	Region       string
	Endpoint     string
	UsePathStyle bool
	// AllowInsecureEndpoint permits an http:// endpoint only for localhost,
	// loopback, or private-IP development targets. It is false by default and
	// never permits a public HTTP endpoint.
	AllowInsecureEndpoint bool
	Credentials           aws.CredentialsProvider
	HTTPClient            aws.HTTPClient
	Retry                 RetryPolicy
}

// TencentCOSConfig remains only as a source-compatibility helper for callers
// migrating to COSConfig. NewS3BlobStore deliberately rejects the resulting
// COS endpoint; Carbon never pretends the AWS SDK's S3 compatibility path is a
// COS-native implementation.
//
// Deprecated: use NewCOSBlobStore with COSConfig.
func TencentCOSConfig(bucket, region, endpoint string, credentials aws.CredentialsProvider) S3Config {
	return S3Config{
		Bucket:       bucket,
		Region:       region,
		Endpoint:     endpoint,
		UsePathStyle: true,
		Credentials:  credentials,
	}
}

// S3BlobStore is an immutable BlobStore over AWS S3 or a compatible endpoint.
// Constructing it performs no remote request; an explicit Repository.Upload
// with UploadOptions.Enabled is still required to publish a snapshot.
type S3BlobStore struct {
	client s3Client
	bucket string
	prefix string
	retry  RetryPolicy
}

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// NewS3BlobStore validates remote routing settings and creates an SDK client.
// It deliberately requires an injected credential provider so credentials are
// resolved outside Carbon configuration and command-line arguments.
func NewS3BlobStore(config S3Config) (*S3BlobStore, error) {
	config.Bucket = strings.TrimSpace(config.Bucket)
	config.Region = strings.ToLower(strings.TrimSpace(config.Region))
	if !validAWSBucket(config.Bucket) {
		return nil, errors.New("backup s3 bucket is invalid")
	}
	if !remoteRegionPattern.MatchString(config.Region) {
		return nil, errors.New("backup s3 region is invalid")
	}
	if config.Credentials == nil {
		return nil, errors.New("backup s3 credentials provider is nil")
	}
	prefix, err := normalizeS3Prefix(config.Prefix)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	if endpoint != "" {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, errors.New("backup s3 endpoint is invalid")
		}
		host := parsed.Hostname()
		if isTrustedAWSEndpoint(host) {
			if parsed.Scheme != "https" || !standardHTTPSPort(parsed) || config.AllowInsecureEndpoint {
				return nil, errors.New("backup AWS endpoint must use HTTPS")
			}
		} else if !isLocalOrPrivateHost(host) || !config.AllowInsecureEndpoint || !config.UsePathStyle {
			return nil, errors.New("backup S3 custom endpoint must be explicit local/private development")
		}
	}
	awsConfig := aws.Config{
		Region:           config.Region,
		Credentials:      config.Credentials,
		HTTPClient:       config.HTTPClient,
		RetryMaxAttempts: 1, // Retry below owns the bounded whole-operation policy.
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = config.UsePathStyle
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &S3BlobStore{client: client, bucket: config.Bucket, prefix: prefix, retry: config.Retry}, nil
}

func (s *S3BlobStore) PutIfAbsent(ctx context.Context, key string, data []byte, opts PutOptions) (BlobInfo, bool, error) {
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
	checksumB64, err := checksumBase64(checksum)
	if err != nil {
		return BlobInfo{}, false, err
	}
	alreadyPresent := false
	err = Retry(ctx, s.retry, func(ctx context.Context) error {
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:            aws.String(s.bucket),
			Key:               aws.String(s.remoteKey(key)),
			Body:              bytes.NewReader(data),
			ContentLength:     aws.Int64(int64(len(data))),
			IfNoneMatch:       aws.String("*"),
			ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
			ChecksumSHA256:    aws.String(checksumB64),
			Metadata:          map[string]string{s3ChecksumMetadataKey: checksum},
		})
		if isS3PreconditionFailed(err) {
			alreadyPresent = true
			return errS3AlreadyPresent
		}
		if isS3ConditionalConflict(err) {
			return s3RetryableError{err: err}
		}
		return err
	})
	if err != nil && !errors.Is(err, errS3AlreadyPresent) {
		return BlobInfo{}, false, err
	}
	if !alreadyPresent {
		return BlobInfo{Key: key, Size: int64(len(data)), SHA256: checksum}, true, nil
	}
	// A remote object may have been created by a concurrent writer. Read and
	// compare it so `created=false` is only idempotent for identical bytes.
	existing, info, err := s.Get(ctx, key)
	if err != nil {
		return BlobInfo{}, false, err
	}
	if !bytes.Equal(existing, data) {
		return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
	}
	return info, false, nil
}

func (s *S3BlobStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	var data []byte
	err := Retry(ctx, s.retry, func(ctx context.Context) error {
		output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.remoteKey(key)),
		})
		if err != nil {
			if isS3NotFound(err) {
				return fmt.Errorf("%w: %s", ErrNotFound, key)
			}
			return err
		}
		if output == nil || output.Body == nil {
			return fmt.Errorf("%w: empty s3 get response", ErrChecksumMismatch)
		}
		body, readErr := io.ReadAll(output.Body)
		closeErr := output.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if output.ContentLength != nil && *output.ContentLength != int64(len(body)) {
			return fmt.Errorf("%w: s3 content length", ErrChecksumMismatch)
		}
		checksum := SHA256Hex(body)
		if err := validateReturnedS3Checksum(checksum, output.Metadata, output.ChecksumSHA256); err != nil {
			return err
		}
		data = body
		return nil
	})
	if err != nil {
		return nil, BlobInfo{}, err
	}
	return data, blobInfo(key, data), nil
}

func (s *S3BlobStore) Stat(ctx context.Context, key string) (BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return BlobInfo{}, err
	}
	var info BlobInfo
	err := Retry(ctx, s.retry, func(ctx context.Context) error {
		output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.remoteKey(key)),
		})
		if err != nil {
			if isS3NotFound(err) {
				return fmt.Errorf("%w: %s", ErrNotFound, key)
			}
			return err
		}
		if output == nil || output.ContentLength == nil || *output.ContentLength < 0 {
			return fmt.Errorf("%w: missing s3 content length", ErrChecksumMismatch)
		}
		checksum, err := returnedS3Checksum(output.Metadata, output.ChecksumSHA256)
		if err != nil {
			return err
		}
		info = BlobInfo{Key: key, Size: *output.ContentLength, SHA256: checksum}
		return nil
	})
	if err != nil {
		return BlobInfo{}, err
	}
	return info, nil
}

// List enumerates a prefix through ListObjectsV2 and follows all continuation
// tokens internally. Object listings do not carry user metadata, so SHA256 is
// intentionally empty here; callers that need trusted content must Get it.
func (s *S3BlobStore) List(ctx context.Context, prefix string) ([]BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBlobPrefix(prefix); err != nil {
		return nil, err
	}
	remotePrefix := s.remotePrefix(prefix)
	continuation := ""
	seenTokens := make(map[string]struct{})
	infos := make([]BlobInfo, 0)
	for {
		var output *s3.ListObjectsV2Output
		err := Retry(ctx, s.retry, func(ctx context.Context) error {
			input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(remotePrefix)}
			if continuation != "" {
				input.ContinuationToken = aws.String(continuation)
			}
			var err error
			output, err = s.client.ListObjectsV2(ctx, input)
			return err
		})
		if err != nil {
			return nil, err
		}
		if output == nil {
			return nil, errors.New("backup s3 list returned an empty response")
		}
		for _, object := range output.Contents {
			remoteKey := aws.ToString(object.Key)
			key, ok := s.localKey(remoteKey)
			if !ok || !strings.HasPrefix(key, prefix) {
				return nil, fmt.Errorf("%w: unexpected s3 listed key", ErrInvalidKey)
			}
			if err := validateBlobKey(key); err != nil {
				return nil, err
			}
			size := int64(0)
			if object.Size != nil {
				size = *object.Size
			}
			if size < 0 {
				return nil, fmt.Errorf("%w: invalid s3 listed size", ErrChecksumMismatch)
			}
			infos = append(infos, BlobInfo{Key: key, Size: size})
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		next := aws.ToString(output.NextContinuationToken)
		if next == "" {
			return nil, errors.New("backup s3 list returned a truncated page without continuation token")
		}
		if _, exists := seenTokens[next]; exists {
			return nil, errors.New("backup s3 list repeated a continuation token")
		}
		seenTokens[next] = struct{}{}
		continuation = next
	}
	return infos, nil
}

func (s *S3BlobStore) remoteKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3BlobStore) remotePrefix(prefix string) string {
	if s.prefix == "" {
		return prefix
	}
	if prefix == "" {
		return s.prefix + "/"
	}
	return s.prefix + "/" + prefix
}

func (s *S3BlobStore) localKey(remoteKey string) (string, bool) {
	if s.prefix == "" {
		return remoteKey, true
	}
	prefix := s.prefix + "/"
	if !strings.HasPrefix(remoteKey, prefix) {
		return "", false
	}
	return strings.TrimPrefix(remoteKey, prefix), true
}

func normalizeS3Prefix(prefix string) (string, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return "", nil
	}
	if err := validateBlobKey(prefix); err != nil {
		return "", fmt.Errorf("backup s3 prefix: %w", err)
	}
	return prefix, nil
}

func isLocalOrPrivateHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

var errS3AlreadyPresent = errors.New("backup s3 object already present")

func isS3PreconditionFailed(err error) bool {
	if err == nil {
		return false
	}
	var apiError apiErrorCoder
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "PreconditionFailed":
			return true
		}
	}
	var status httpStatusCoder
	return errors.As(err, &status) && status.HTTPStatusCode() == 412
}

func isS3ConditionalConflict(err error) bool {
	if err == nil {
		return false
	}
	var apiError apiErrorCoder
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "ConditionalRequestConflict", "OperationAborted":
			return true
		}
	}
	var status httpStatusCoder
	return errors.As(err, &status) && status.HTTPStatusCode() == 409
}

type s3RetryableError struct{ err error }

func (e s3RetryableError) Error() string   { return e.err.Error() }
func (e s3RetryableError) Unwrap() error   { return e.err }
func (e s3RetryableError) Retryable() bool { return true }

type apiErrorCoder interface{ ErrorCode() string }

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiError apiErrorCoder
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NoSuchBucket", "NotFound", "NoSuchObject":
			return true
		}
	}
	var status httpStatusCoder
	return errors.As(err, &status) && status.HTTPStatusCode() == 404
}

func checksumBase64(checksum string) (string, error) {
	if err := validateSHA256(checksum); err != nil {
		return "", err
	}
	decoded, err := hex.DecodeString(checksum)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

func returnedS3Checksum(metadata map[string]string, checksumB64 *string) (string, error) {
	if checksum := metadata[s3ChecksumMetadataKey]; checksum != "" {
		if err := validateSHA256(checksum); err != nil {
			return "", fmt.Errorf("%w: invalid s3 metadata checksum", ErrChecksumMismatch)
		}
		return checksum, nil
	}
	if checksum := metadata[legacyS3ChecksumMetadataKey]; checksum != "" {
		if err := validateSHA256(checksum); err != nil {
			return "", fmt.Errorf("%w: invalid legacy s3 metadata checksum", ErrChecksumMismatch)
		}
		return checksum, nil
	}
	if checksumB64 != nil && *checksumB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(*checksumB64)
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("%w: invalid s3 response checksum", ErrChecksumMismatch)
		}
		return hex.EncodeToString(decoded), nil
	}
	return "", fmt.Errorf("%w: s3 object has no SHA-256 checksum", ErrChecksumMismatch)
}

func validateReturnedS3Checksum(actual string, metadata map[string]string, checksumB64 *string) error {
	expected, err := returnedS3Checksum(metadata, checksumB64)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: s3 object checksum", ErrChecksumMismatch)
	}
	return nil
}
