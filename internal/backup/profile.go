package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// RemoteProfile is the persisted, credential-free description of one remote
// backup destination. CredentialRef and EncryptionKeyRef are opaque references;
// their resolved values are deliberately never marshalled, logged, or retained
// outside an explicit upload operation.
type RemoteProfile struct {
	Profile string `json:"profile,omitempty"`
	Enabled bool   `json:"enabled"`
	// ContinuousAuthorization is a durable, separately-confirmed consent flag.
	// It permits a host's scheduled sync callback to contact a provider only
	// after the local snapshot has been verified. Manual local runs and all
	// configuration/status operations remain provider-free.
	ContinuousAuthorization bool   `json:"continuousAuthorization"`
	Backend                 string `json:"backend,omitempty"`
	Bucket                  string `json:"bucket,omitempty"`
	Prefix                  string `json:"prefix,omitempty"`
	Region                  string `json:"region,omitempty"`
	Endpoint                string `json:"endpoint,omitempty"`
	UsePathStyle            bool   `json:"usePathStyle,omitempty"`
	AllowInsecureEndpoint   bool   `json:"allowInsecureEndpoint,omitempty"`
	CredentialRef           string `json:"credentialRef,omitempty"`
	Encryption              bool   `json:"encryption"`
	EncryptionKeyRef        string `json:"encryptionKeyRef,omitempty"`
}

var (
	remoteProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	remoteRegionPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	environmentNamePattern   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	awsBucketPattern         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{1,61})[a-z0-9]$`)
	cosBucketPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,53}-[0-9]{6,20}$`)
)

// NormalizeRemoteProfile validates and canonicalizes a profile without ever
// resolving references. Calling it is therefore safe on HTTP GET/PUT, status,
// and local-snapshot paths.
func NormalizeRemoteProfile(profile *RemoteProfile) error {
	if profile == nil {
		return errors.New("backup profile is required")
	}
	p := profile
	p.Profile = strings.TrimSpace(p.Profile)
	p.Backend = strings.ToLower(strings.TrimSpace(p.Backend))
	p.Bucket = strings.TrimSpace(p.Bucket)
	p.Prefix = strings.Trim(strings.TrimSpace(p.Prefix), "/")
	p.Region = strings.ToLower(strings.TrimSpace(p.Region))
	p.Endpoint = strings.TrimRight(strings.TrimSpace(p.Endpoint), "/")
	p.CredentialRef = strings.TrimSpace(p.CredentialRef)
	p.EncryptionKeyRef = strings.TrimSpace(p.EncryptionKeyRef)
	if p.Backend == "" {
		p.Backend = "s3"
	}
	if p.Backend != "s3" && p.Backend != "cos" {
		return errors.New("backup backend must be s3 or cos")
	}
	if p.Profile != "" && !remoteProfileNamePattern.MatchString(p.Profile) {
		return errors.New("backup profile name is invalid")
	}

	hasDetails := p.hasRemoteDetails()
	if !hasDetails {
		if p.Enabled {
			return errors.New("enabled backup remote is not configured")
		}
		if p.AllowInsecureEndpoint || p.UsePathStyle || p.Encryption || p.CredentialRef != "" || p.EncryptionKeyRef != "" {
			return errors.New("backup remote settings require a destination")
		}
		return nil
	}
	if p.Bucket == "" || p.Region == "" || p.CredentialRef == "" {
		return errors.New("configured backup remote requires bucket, region, and credentialRef")
	}
	if !remoteRegionPattern.MatchString(p.Region) {
		return errors.New("backup region is invalid")
	}
	if p.Backend == "s3" {
		if !validAWSBucket(p.Bucket) {
			return errors.New("backup S3 bucket is invalid")
		}
	} else if !cosBucketPattern.MatchString(p.Bucket) {
		return errors.New("backup COS bucket must include its APPID")
	}
	if p.Prefix != "" {
		prefix, err := normalizeS3Prefix(p.Prefix)
		if err != nil {
			return errors.New("backup prefix is invalid")
		}
		p.Prefix = prefix
	}
	if !validCredentialReference(p.Backend, p.CredentialRef) {
		return errors.New("backup credentialRef is invalid")
	}
	if !p.Encryption || !validEncryptionKeyReference(p.EncryptionKeyRef) {
		return errors.New("configured backup remote requires encryption and encryptionKeyRef")
	}
	if err := validateRemoteEndpoint(p); err != nil {
		return err
	}
	if p.Enabled && (!p.Encryption || p.EncryptionKeyRef == "") {
		return errors.New("enabled backup remote requires encryption")
	}
	if p.ContinuousAuthorization && !p.Enabled {
		return errors.New("continuous backup authorization requires an enabled remote")
	}
	return nil
}

// Configured reports whether the profile has every non-secret field needed for
// an explicit upload. It does not resolve credentials or probe the network.
func (p RemoteProfile) Configured() bool {
	copy := p
	return NormalizeRemoteProfile(&copy) == nil && copy.Bucket != "" && copy.Region != "" && copy.CredentialRef != "" && copy.Encryption && copy.EncryptionKeyRef != ""
}

// ContinuousSyncEnabled reports whether this profile is allowed to make a
// scheduled, encrypted remote sync. It validates only persisted values and
// never resolves a credential or key reference.
func (p RemoteProfile) ContinuousSyncEnabled() bool {
	return p.Enabled && p.ContinuousAuthorization && p.Configured()
}

// RemoteDestinationFingerprint identifies the non-secret encrypted destination
// used for an automatic sync. It intentionally excludes credential references
// but includes the encryption-key reference: changing either the target or the
// encryption key must cause the current snapshot to be republished. The hash is
// safe to retain in private runtime state and avoids storing a provider URL,
// bucket, or key reference there.
func RemoteDestinationFingerprint(profile RemoteProfile) (string, error) {
	p := profile
	if err := NormalizeRemoteProfile(&p); err != nil {
		return "", err
	}
	if !p.ContinuousSyncEnabled() {
		return "", errors.New("continuous backup sync is not authorized")
	}
	canonical := strings.Join([]string{
		"carbon-backup-remote-v1",
		p.Backend,
		p.Bucket,
		p.Prefix,
		p.Region,
		p.Endpoint,
		strconv.FormatBool(p.UsePathStyle),
		strconv.FormatBool(p.AllowInsecureEndpoint),
		p.EncryptionKeyRef,
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

func (p RemoteProfile) hasRemoteDetails() bool {
	return p.Bucket != "" || p.Prefix != "" || p.Region != "" || p.Endpoint != "" || p.CredentialRef != "" || p.EncryptionKeyRef != "" || p.UsePathStyle || p.AllowInsecureEndpoint || p.Encryption || p.ContinuousAuthorization
}

func validAWSBucket(bucket string) bool {
	if !awsBucketPattern.MatchString(bucket) || strings.Contains(bucket, "..") {
		return false
	}
	return net.ParseIP(bucket) == nil
}

func validCredentialReference(backend, reference string) bool {
	if reference == "aws-default://" {
		return backend == "s3"
	}
	if name, ok := referenceSuffix(reference, "aws-profile://"); ok {
		return backend == "s3" && remoteProfileNamePattern.MatchString(name)
	}
	if _, ok := namedReference(reference, "env://"); ok {
		return true
	}
	if _, ok := namedReference(reference, "cos-env://"); ok {
		return backend == "cos"
	}
	return false
}

func validEncryptionKeyReference(reference string) bool {
	_, ok := namedReference(reference, "env://")
	return ok
}

func namedReference(reference, prefix string) (string, bool) {
	name, ok := referenceSuffix(reference, prefix)
	return name, ok && environmentNamePattern.MatchString(name)
}

func referenceSuffix(reference, prefix string) (string, bool) {
	if !strings.HasPrefix(reference, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(reference, prefix)
	return name, name != ""
}

func validateRemoteEndpoint(profile *RemoteProfile) error {
	if profile.Endpoint == "" {
		if profile.Backend == "cos" {
			return errors.New("configured COS backup requires an endpoint")
		}
		if profile.AllowInsecureEndpoint {
			return errors.New("backup insecure-endpoint mode requires a local endpoint")
		}
		return nil
	}
	parsed, err := url.ParseRequestURI(profile.Endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("backup endpoint is invalid")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("backup endpoint must use HTTPS")
	}
	if profile.Backend == "cos" {
		if parsed.Scheme != "https" || !standardHTTPSPort(parsed) || profile.AllowInsecureEndpoint || profile.UsePathStyle || !isTrustedCOSEndpoint(host, profile.Region) {
			return errors.New("backup COS endpoint must be the matching official HTTPS regional endpoint")
		}
		return nil
	}
	if isTrustedAWSEndpoint(host) {
		if parsed.Scheme != "https" || !standardHTTPSPort(parsed) || profile.AllowInsecureEndpoint {
			return errors.New("backup AWS endpoint must use HTTPS")
		}
		return nil
	}
	if !isLocalOrPrivateHost(host) || !profile.AllowInsecureEndpoint || !profile.UsePathStyle || !isEnvironmentCredentialReference(profile.CredentialRef) {
		return errors.New("backup custom endpoint must be explicit local development with env credentials")
	}
	// Local development may use HTTP for MinIO/LocalStack, but callers must opt
	// in with AllowInsecureEndpoint and cannot use cloud default/profile creds.
	return nil
}

// standardHTTPSPort accepts an omitted port or the conventional explicit 443.
// A different port turns an otherwise familiar cloud hostname into a custom
// endpoint and must not be treated as a trusted credential destination.
func standardHTTPSPort(endpoint *url.URL) bool {
	return endpoint.Port() == "" || endpoint.Port() == "443"
}

func isEnvironmentCredentialReference(reference string) bool {
	_, env := namedReference(reference, "env://")
	return env
}

func isTrustedAWSEndpoint(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !(strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn")) {
		return false
	}
	label := strings.Split(host, ".")[0]
	return label == "s3" || strings.HasPrefix(label, "s3-")
}

func isTrustedCOSEndpoint(host, region string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "cos."+region+".myqcloud.com" || host == "cos."+region+".tencentcos.cn"
}

// NewEncryptedRemoteBlobStore resolves the selected credential and key
// references only for an already-authorized upload. It returns a client-side
// encrypted store unconditionally; no code path in this factory can produce a
// plaintext-capable remote store.
func NewEncryptedRemoteBlobStore(ctx context.Context, profile RemoteProfile) (BlobStore, error) {
	p := profile
	if err := NormalizeRemoteProfile(&p); err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrRemoteDisabled
	}

	var raw BlobStore
	var err error
	switch p.Backend {
	case "s3":
		provider, resolveErr := resolveAWSCredentials(ctx, p.CredentialRef, p.Region)
		if resolveErr != nil {
			return nil, resolveErr
		}
		raw, err = NewS3BlobStore(S3Config{
			Bucket:                p.Bucket,
			Prefix:                p.Prefix,
			Region:                p.Region,
			Endpoint:              p.Endpoint,
			UsePathStyle:          p.UsePathStyle,
			AllowInsecureEndpoint: p.AllowInsecureEndpoint,
			Credentials:           provider,
		})
	case "cos":
		credentials, resolveErr := resolveCOSCredentials(p.CredentialRef)
		if resolveErr != nil {
			return nil, resolveErr
		}
		raw, err = NewCOSBlobStore(COSConfig{
			Bucket:      p.Bucket,
			Prefix:      p.Prefix,
			Region:      p.Region,
			Endpoint:    p.Endpoint,
			Credentials: credentials,
		})
	default:
		return nil, errors.New("backup backend is invalid")
	}
	if err != nil {
		return nil, err
	}
	return NewEncryptedBlobStore(raw, environmentKeyProvider{}, KeyReference(p.EncryptionKeyRef))
}

func resolveAWSCredentials(ctx context.Context, reference, region string) (aws.CredentialsProvider, error) {
	switch {
	case reference == "aws-default://":
		config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil || config.Credentials == nil {
			return nil, errors.New("backup AWS credential resolution failed")
		}
		return config.Credentials, nil
	case strings.HasPrefix(reference, "aws-profile://"):
		name, ok := referenceSuffix(reference, "aws-profile://")
		if !ok || !remoteProfileNamePattern.MatchString(name) {
			return nil, errors.New("backup AWS credential reference is invalid")
		}
		config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region), awsconfig.WithSharedConfigProfile(name))
		if err != nil || config.Credentials == nil {
			return nil, errors.New("backup AWS credential resolution failed")
		}
		return config.Credentials, nil
	default:
		name, ok := namedReference(reference, "env://")
		if !ok {
			return nil, errors.New("backup AWS credential reference is invalid")
		}
		accessKey := os.Getenv(name + "_ACCESS_KEY_ID")
		secretKey := os.Getenv(name + "_SECRET_ACCESS_KEY")
		if accessKey == "" || secretKey == "" {
			return nil, errors.New("backup AWS environment credentials are unavailable")
		}
		return credentials.NewStaticCredentialsProvider(accessKey, secretKey, os.Getenv(name+"_SESSION_TOKEN")), nil
	}
}

// COSCredentials contains short-lived values only while a COS upload client is
// in memory. It has no JSON tags by design and must never be persisted.
type COSCredentials struct {
	SecretID     string `json:"-"`
	SecretKey    string `json:"-"`
	SessionToken string `json:"-"`
}

func resolveCOSCredentials(reference string) (COSCredentials, error) {
	name, ok := namedReference(reference, "cos-env://")
	if !ok {
		name, ok = namedReference(reference, "env://")
	}
	if !ok {
		return COSCredentials{}, errors.New("backup COS credential reference is invalid")
	}
	credentials := COSCredentials{
		SecretID:     os.Getenv(name + "_SECRET_ID"),
		SecretKey:    os.Getenv(name + "_SECRET_KEY"),
		SessionToken: os.Getenv(name + "_SESSION_TOKEN"),
	}
	if credentials.SecretID == "" || credentials.SecretKey == "" {
		return COSCredentials{}, errors.New("backup COS environment credentials are unavailable")
	}
	return credentials, nil
}

type environmentKeyProvider struct{}

func (environmentKeyProvider) Resolve(_ context.Context, reference KeyReference) ([]byte, error) {
	name, ok := namedReference(string(reference), "env://")
	if !ok {
		return nil, errors.New("backup encryption key reference is invalid")
	}
	encoded := os.Getenv(name)
	if encoded == "" {
		return nil, errors.New("backup encryption key is unavailable")
	}
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(encoded); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, fmt.Errorf("%w: environment key must be base64 or hexadecimal AES-256 material", ErrInvalidKeyMaterial)
}
