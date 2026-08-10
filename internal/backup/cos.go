package backup

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

// COSConfig configures Tencent COS through its official v5 SDK. Endpoint must
// be the regional service endpoint (for example
// https://cos.ap-guangzhou.myqcloud.com), not an S3-compatibility endpoint.
// The adapter constructs the bucket virtual-host URL itself.
type COSConfig struct {
	Bucket      string
	Prefix      string
	Region      string
	Endpoint    string
	Credentials COSCredentials
	HTTPClient  *http.Client
	Retry       RetryPolicy
}

// COSBlobStore is an immutable Tencent COS BlobStore. It deliberately does
// not reuse the S3 adapter: COS uses virtual-host routing and its own
// x-cos-forbid-overwrite contract.
type COSBlobStore struct {
	client cosBlobClient
	prefix string
	retry  RetryPolicy
}

type cosBlobClient interface {
	Put(context.Context, string, io.Reader, *cos.ObjectPutOptions) (*cos.Response, error)
	Get(context.Context, string, *cos.ObjectGetOptions) (*cos.Response, error)
	Head(context.Context, string, *cos.ObjectHeadOptions) (*cos.Response, error)
	List(context.Context, *cos.BucketGetOptions) (*cos.BucketGetResult, *cos.Response, error)
}

type sdkCOSBlobClient struct{ client *cos.Client }

func (c sdkCOSBlobClient) Put(ctx context.Context, key string, body io.Reader, options *cos.ObjectPutOptions) (*cos.Response, error) {
	return c.client.Object.Put(ctx, key, body, options)
}

func (c sdkCOSBlobClient) Get(ctx context.Context, key string, options *cos.ObjectGetOptions) (*cos.Response, error) {
	return c.client.Object.Get(ctx, key, options)
}

func (c sdkCOSBlobClient) Head(ctx context.Context, key string, options *cos.ObjectHeadOptions) (*cos.Response, error) {
	return c.client.Object.Head(ctx, key, options)
}

func (c sdkCOSBlobClient) List(ctx context.Context, options *cos.BucketGetOptions) (*cos.BucketGetResult, *cos.Response, error) {
	return c.client.Bucket.Get(ctx, options)
}

// NewCOSBlobStore creates a COS SDK client without issuing a network request.
// Credential strings live only in the authorization transport created here and
// are released with this short-lived explicit-upload store.
func NewCOSBlobStore(config COSConfig) (*COSBlobStore, error) {
	config.Bucket = strings.TrimSpace(config.Bucket)
	config.Region = strings.ToLower(strings.TrimSpace(config.Region))
	config.Endpoint = strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	if !cosBucketPattern.MatchString(config.Bucket) {
		return nil, errors.New("backup COS bucket must include its APPID")
	}
	if !remoteRegionPattern.MatchString(config.Region) {
		return nil, errors.New("backup COS region is invalid")
	}
	if config.Credentials.SecretID == "" || config.Credentials.SecretKey == "" {
		return nil, errors.New("backup COS credentials are unavailable")
	}
	prefix, err := normalizeS3Prefix(config.Prefix)
	if err != nil {
		return nil, errors.New("backup COS prefix is invalid")
	}
	endpoint, err := parseCOSEndpoint(config.Endpoint, config.Region)
	if err != nil {
		return nil, err
	}
	endpoint.Host = config.Bucket + "." + endpoint.Host

	client := clonedCOSHTTPClient(config.HTTPClient)
	client.Transport = &cos.AuthorizationTransport{
		SecretID:     config.Credentials.SecretID,
		SecretKey:    config.Credentials.SecretKey,
		SessionToken: config.Credentials.SessionToken,
		Transport:    client.Transport,
	}
	sdk := cos.NewClient(&cos.BaseURL{BucketURL: endpoint}, client)
	// The generic retry helper below is the single bounded retry policy. Do not
	// let the SDK retry a body independently and risk surprising extra writes.
	sdk.Conf.RetryOpt.Count = 1
	return newCOSBlobStore(sdkCOSBlobClient{client: sdk}, prefix, config.Retry), nil
}

func newCOSBlobStore(client cosBlobClient, prefix string, retry RetryPolicy) *COSBlobStore {
	return &COSBlobStore{client: client, prefix: prefix, retry: retry}
}

func clonedCOSHTTPClient(input *http.Client) *http.Client {
	if input == nil {
		return &http.Client{}
	}
	clone := *input
	return &clone
}

func parseCOSEndpoint(raw, region string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || !standardHTTPSPort(parsed) || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || !isTrustedCOSEndpoint(parsed.Hostname(), region) {
		return nil, errors.New("backup COS endpoint must be the matching official HTTPS regional endpoint")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func (s *COSBlobStore) PutIfAbsent(ctx context.Context, key string, data []byte, options PutOptions) (BlobInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, false, err
	}
	if err := validateBlobKey(key); err != nil {
		return BlobInfo{}, false, err
	}
	checksum, err := validatePut(data, options)
	if err != nil {
		return BlobInfo{}, false, err
	}
	// A read before the conditional write avoids unnecessary upload and makes an
	// existing immutable object idempotent. The x-cos-forbid-overwrite header
	// below is still required to close the race between this read and Put.
	if existing, info, getErr := s.Get(ctx, key); getErr == nil {
		if !bytes.Equal(existing, data) {
			return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
		}
		return info, false, nil
	} else if !errors.Is(getErr, ErrNotFound) {
		return BlobInfo{}, false, getErr
	}

	header := make(http.Header)
	header.Set("x-cos-forbid-overwrite", "true")
	header.Set("x-cos-content-sha1", sha1Hex(data))
	putOptions := &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
		ContentLength: int64(len(data)),
		ContentType:   "application/octet-stream",
		XOptionHeader: &header,
	}}
	remoteKey := s.remoteKey(key)
	alreadyPresent := false
	err = Retry(ctx, s.retry, func(ctx context.Context) error {
		response, putErr := s.client.Put(ctx, remoteKey, bytes.NewReader(data), putOptions)
		closeCOSResponse(response)
		if isCOSPreconditionFailed(putErr) {
			alreadyPresent = true
			return errS3AlreadyPresent
		}
		if isCOSConditionalConflict(putErr) {
			return s3RetryableError{err: putErr}
		}
		return putErr
	})
	if err != nil && !errors.Is(err, errS3AlreadyPresent) {
		return BlobInfo{}, false, err
	}
	if alreadyPresent {
		existing, info, getErr := s.Get(ctx, key)
		if getErr != nil {
			return BlobInfo{}, false, getErr
		}
		if !bytes.Equal(existing, data) {
			return BlobInfo{}, false, fmt.Errorf("%w: %s", ErrImmutableConflict, key)
		}
		return info, false, nil
	}
	// COS accepts a server-side checksum header, but independently fetching the
	// object verifies the response body and catches a bad/misrouted provider.
	written, info, err := s.Get(ctx, key)
	if err != nil {
		return BlobInfo{}, false, err
	}
	if !bytes.Equal(written, data) || info.SHA256 != checksum {
		return BlobInfo{}, false, fmt.Errorf("%w: COS object body verification failed", ErrChecksumMismatch)
	}
	return info, true, nil
}

func (s *COSBlobStore) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	var data []byte
	err := Retry(ctx, s.retry, func(ctx context.Context) error {
		response, getErr := s.client.Get(ctx, s.remoteKey(key), nil)
		if getErr != nil {
			if isCOSNotFound(getErr) {
				return fmt.Errorf("%w: %s", ErrNotFound, key)
			}
			return getErr
		}
		if response == nil || response.Response == nil || response.Body == nil {
			return fmt.Errorf("%w: empty COS get response", ErrChecksumMismatch)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := validateCOSResponseBody(response.Header, body); err != nil {
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

func (s *COSBlobStore) Stat(ctx context.Context, key string) (BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	if err := validateBlobKey(key); err != nil {
		return BlobInfo{}, err
	}
	var length int64
	err := Retry(ctx, s.retry, func(ctx context.Context) error {
		response, headErr := s.client.Head(ctx, s.remoteKey(key), nil)
		if headErr != nil {
			if isCOSNotFound(headErr) {
				return fmt.Errorf("%w: %s", ErrNotFound, key)
			}
			return headErr
		}
		if response == nil || response.Response == nil {
			return fmt.Errorf("%w: empty COS head response", ErrChecksumMismatch)
		}
		parsed, parseErr := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		closeCOSResponse(response)
		if parseErr != nil || parsed < 0 {
			return fmt.Errorf("%w: invalid COS content length", ErrChecksumMismatch)
		}
		length = parsed
		return nil
	})
	if err != nil {
		return BlobInfo{}, err
	}
	// COS Head does not expose a SHA-256 checksum. Read the immutable object and
	// calculate it ourselves rather than relying on x-amz-compatible metadata.
	_, info, err := s.Get(ctx, key)
	if err != nil {
		return BlobInfo{}, err
	}
	if info.Size != length {
		return BlobInfo{}, fmt.Errorf("%w: COS stat/get size mismatch", ErrChecksumMismatch)
	}
	return info, nil
}

func (s *COSBlobStore) List(ctx context.Context, prefix string) ([]BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBlobPrefix(prefix); err != nil {
		return nil, err
	}
	remotePrefix := s.remotePrefix(prefix)
	marker := ""
	seenMarkers := make(map[string]struct{})
	infos := make([]BlobInfo, 0)
	for {
		var output *cos.BucketGetResult
		err := Retry(ctx, s.retry, func(ctx context.Context) error {
			var callErr error
			output, _, callErr = s.client.List(ctx, &cos.BucketGetOptions{Prefix: remotePrefix, Marker: marker})
			return callErr
		})
		if err != nil {
			return nil, err
		}
		if output == nil {
			return nil, errors.New("backup COS list returned an empty response")
		}
		for _, object := range output.Contents {
			key, ok := s.localKey(object.Key)
			if !ok || !strings.HasPrefix(key, prefix) || validateBlobKey(key) != nil || object.Size < 0 {
				return nil, fmt.Errorf("%w: invalid COS listed object", ErrInvalidKey)
			}
			infos = append(infos, BlobInfo{Key: key, Size: object.Size})
		}
		if !output.IsTruncated {
			break
		}
		next := output.NextMarker
		if next == "" {
			return nil, errors.New("backup COS list returned a truncated page without a marker")
		}
		if _, exists := seenMarkers[next]; exists {
			return nil, errors.New("backup COS list repeated a marker")
		}
		seenMarkers[next] = struct{}{}
		marker = next
	}
	return infos, nil
}

func (s *COSBlobStore) remoteKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *COSBlobStore) remotePrefix(prefix string) string {
	if s.prefix == "" {
		return prefix
	}
	if prefix == "" {
		return s.prefix + "/"
	}
	return s.prefix + "/" + prefix
}

func (s *COSBlobStore) localKey(remoteKey string) (string, bool) {
	if s.prefix == "" {
		return remoteKey, true
	}
	prefix := s.prefix + "/"
	if !strings.HasPrefix(remoteKey, prefix) {
		return "", false
	}
	return strings.TrimPrefix(remoteKey, prefix), true
}

func sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

func validateCOSResponseBody(header http.Header, data []byte) error {
	if rawLength := header.Get("Content-Length"); rawLength != "" {
		length, err := strconv.ParseInt(rawLength, 10, 64)
		if err != nil || length != int64(len(data)) {
			return fmt.Errorf("%w: COS content length", ErrChecksumMismatch)
		}
	}
	if returned := header.Get("x-cos-content-sha1"); returned != "" && !strings.EqualFold(returned, sha1Hex(data)) {
		return fmt.Errorf("%w: COS content SHA-1", ErrChecksumMismatch)
	}
	return nil
}

func closeCOSResponse(response *cos.Response) {
	if response != nil && response.Response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func isCOSNotFound(err error) bool {
	if cos.IsNotFoundError(err) {
		return true
	}
	return cosHTTPStatus(err) == http.StatusNotFound
}

func isCOSPreconditionFailed(err error) bool {
	return cosHTTPStatus(err) == http.StatusPreconditionFailed
}

func isCOSConditionalConflict(err error) bool {
	return cosHTTPStatus(err) == http.StatusConflict
}

func cosHTTPStatus(err error) int {
	if err == nil {
		return 0
	}
	var response *cos.ErrorResponse
	if errors.As(err, &response) && response != nil && response.Response != nil {
		return response.Response.StatusCode
	}
	var coded interface{ HTTPStatusCode() int }
	if errors.As(err, &coded) {
		return coded.HTTPStatusCode()
	}
	return 0
}
